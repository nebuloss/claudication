package secret

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A damaged key file must stop startup, not be replaced: a fresh key would
// silently orphan every credential sealed with the old one, and the error has
// to say that removing it costs exactly that.
func TestCorruptKeyFileIsRefusedNotReplaced(t *testing.T) {
	for name, content := range map[string]string{
		"not base64":   "%%% definitely not base64 %%%",
		"wrong length": base64.StdEncoding.EncodeToString([]byte("sixteen byte key")),
		"empty":        "",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(KeyEnv, "")
			dir := t.TempDir()
			path := filepath.Join(dir, keyFile)
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(dir)
			if err == nil || !strings.Contains(err.Error(), "corrupt") {
				t.Fatalf("Load = %v, want a corrupt-key error", err)
			}
			after, _ := os.ReadFile(path)
			if string(after) != content {
				t.Error("the corrupt key file was overwritten")
			}
		})
	}
}

// A key file that exists but cannot be read is not a first run. Treating it as
// one would generate a new key over the old one's credentials.
func TestUnreadableKeyFileIsAnError(t *testing.T) {
	t.Setenv(KeyEnv, "")
	dir := t.TempDir()
	// A directory where the file should be: present, and unreadable as a file,
	// whoever runs the test.
	if err := os.Mkdir(filepath.Join(dir, keyFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "read") {
		t.Fatalf("Load = %v, want a read error", err)
	}
}

// A state directory that does not exist cannot hold the key, and saying so is
// better than running with one that vanishes on restart.
func TestMissingStateDirIsAnError(t *testing.T) {
	t.Setenv(KeyEnv, "")
	dir := filepath.Join(t.TempDir(), "absent")
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "create") {
		t.Fatalf("Load = %v, want a create error", err)
	}
}

// The key written on first run is the one that sealed the data, so reading the
// file back must yield a sealer that opens it — checked against the file's own
// bytes, not just a second Load.
func TestGeneratedKeyFileIsTheKeyInUse(t *testing.T) {
	t.Setenv(KeyEnv, "")
	dir := t.TempDir()
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := s.Seal("payload")
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, keyFile))
	if err != nil {
		t.Fatal(err)
	}
	key, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil || len(key) != keyBytes {
		t.Fatalf("key file holds %d bytes (%v), want %d", len(key), err, keyBytes)
	}
	if bytes.Equal(key, make([]byte, keyBytes)) {
		t.Fatal("generated key is all zeroes")
	}

	// The same key injected through the environment must open it too: that is
	// how an operator moves a deployment to a vault.
	t.Setenv(KeyEnv, string(data))
	other, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := other.Open(sealed); err != nil || got != "payload" {
		t.Errorf("Open with the same key from the environment = %q, %v", got, err)
	}
}
