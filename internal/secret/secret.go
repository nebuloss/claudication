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
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
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
	// key is kept to derive purpose keys from (Derive, Hasher).
	key []byte
}

// Load resolves the instance key from the environment, or from stateDir,
// generating it on first run.
func Load(stateDir string) (*Sealer, error) {
	key, err := resolveKey(stateDir)
	if err != nil {
		return nil, err
	}
	return newSealer(key)
}

func newSealer(key []byte) (*Sealer, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("init cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("init GCM: %w", err)
	}
	return &Sealer{aead: aead, key: key}, nil
}

// derived is a key for one purpose, from the instance key: HKDF-SHA256 with
// the purpose as its info, so no two purposes share a key.
func (s *Sealer) derived(purpose string) ([]byte, error) {
	k, err := hkdf.Key(sha256.New, s.key, nil, "claudication "+purpose, keyBytes)
	if err != nil {
		return nil, fmt.Errorf("derive a key for %s: %w", purpose, err)
	}
	return k, nil
}

// Derive is a sealer of its own for one purpose — recorded content, say —
// so what it seals cannot be opened as a credential, nor a credential as it.
func (s *Sealer) Derive(purpose string) (*Sealer, error) {
	k, err := s.derived(purpose)
	if err != nil {
		return nil, err
	}
	return newSealer(k)
}

// Hasher is a keyed hash for one purpose: HMAC-SHA256 truncated to 16 bytes.
// Keyed, so that two equal contents share a hash without a hash revealing a
// content anyone could guess and hash themselves.
func (s *Sealer) Hasher(purpose string) (func([]byte) []byte, error) {
	k, err := s.derived(purpose)
	if err != nil {
		return nil, err
	}
	return func(b []byte) []byte {
		m := hmac.New(sha256.New, k)
		m.Write(b)
		return m.Sum(nil)[:16]
	}, nil
}

// SealBytes encrypts plaintext bound to aad, returning nonce||ciphertext.
// The aad is not stored: opening needs the same, which ties a sealed value
// to where it belongs (its hash, its row).
func (s *Sealer) SealBytes(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize(), s.aead.NonceSize()+len(plaintext)+s.aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	return s.aead.Seal(nonce, nonce, plaintext, aad), nil
}

// OpenBytes reverses SealBytes.
func (s *Sealer) OpenBytes(sealed, aad []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("sealed value is too short")
	}
	out, err := s.aead.Open(nil, sealed[:n], sealed[n:], aad)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w (wrong instance key?)", err)
	}
	return out, nil
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
	switch err := createKeyFile(stateDir, path, encoded); {
	case errors.Is(err, os.ErrExist):
		// Another process starting at the same moment created it first: its
		// key is the one in use, so read it rather than overwrite it.
		return resolveKey(stateDir)
	case err != nil:
		return nil, err
	}
	return key, nil
}

// createKeyFile puts the encoded key at path, whole or not at all, and fails
// with os.ErrExist when something is already there.
//
// Whole or not at all because the file was created empty and written after,
// so a crash between the two left an empty key file — which the next start
// refuses as corrupt, with every stored credential sealed under the key that
// was never written down. So the key is written and synced under a temporary
// name and linked into place. A link, unlike a rename, refuses to replace a
// name that exists, which keeps the rule O_EXCL gave: if two processes start
// at once, exactly one creates the key and the other reads it.
//
// A filesystem that cannot link falls back to creating the file in place with
// O_EXCL, which is what it did before.
func createKeyFile(dir, path, encoded string) error {
	tmp, err := os.CreateTemp(dir, keyFile+".new-*")
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(encoded); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	switch err := os.Link(tmp.Name(), path); {
	case err == nil:
		// The name itself has to survive a crash too.
		if d, err := os.Open(dir); err == nil {
			_ = d.Sync()
			d.Close()
		}
		return nil
	case errors.Is(err, os.ErrExist):
		return err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return err
		}
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(encoded); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return f.Sync()
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
