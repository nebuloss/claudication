// Package secret seals credentials before they touch the database.
//
// The state directory is already 0700 and the database 0600, so this is the
// inner layer: it means a leaked database file, a stray backup, or a snapshot
// of the volume is not by itself enough to replay someone's OAuth session.
// The key lives beside the database rather than inside it, so the two must be
// stolen together.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// KeyEnv overrides the on-disk key, for deployments that inject secrets
	// from a vault rather than persisting them next to the data.
	KeyEnv   = "CLAUDICATION_SECRET_KEY"
	keyFile  = "secret.key"
	keyBytes = 32 // AES-256
)

type Sealer struct {
	aead cipher.AEAD
}

// Load resolves the instance key from the environment, or from stateDir,
// generating it on first run.
func Load(stateDir string) (*Sealer, error) {
	key, err := resolveKey(stateDir)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("init cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("init GCM: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

func resolveKey(stateDir string) ([]byte, error) {
	if raw := os.Getenv(KeyEnv); raw != "" {
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("%s must be base64: %w", KeyEnv, err)
		}
		if len(key) != keyBytes {
			return nil, fmt.Errorf("%s must decode to %d bytes, got %d", KeyEnv, keyBytes, len(key))
		}
		return key, nil
	}

	path := filepath.Join(stateDir, keyFile)
	switch data, err := os.ReadFile(path); {
	case err == nil:
		key, decErr := base64.StdEncoding.DecodeString(string(data))
		if decErr != nil || len(key) != keyBytes {
			return nil, fmt.Errorf("%s is corrupt; remove it only if you accept losing every stored credential", path)
		}
		return key, nil
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	key := make([]byte, keyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate instance key: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	// O_EXCL: if two processes start at once, exactly one creates the key and
	// the loser re-reads rather than silently overwriting live credentials.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return resolveKey(stateDir)
		}
		return nil, fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(encoded); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	return key, nil
}

// Seal encrypts plaintext, returning nonce||ciphertext.
func (s *Sealer) Seal(plaintext string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	return s.aead.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// Open reverses Seal.
func (s *Sealer) Open(sealed []byte) (string, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return "", errors.New("sealed value is too short")
	}
	out, err := s.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w (wrong instance key?)", err)
	}
	return string(out), nil
}
