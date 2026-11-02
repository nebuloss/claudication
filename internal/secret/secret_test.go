package secret

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	s, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	const plaintext = "sk-ant-oat01-not-a-real-token"
	sealed, err := s.Seal(plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if strings.Contains(string(sealed), plaintext) {
		t.Fatal("sealed bytes contain the plaintext")
	}

	opened, err := s.Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened != plaintext {
		t.Errorf("Open = %q, want %q", opened, plaintext)
	}
}

// GCM is nonce-randomised, so the same input must not seal to the same bytes.
func TestSealIsNonDeterministic(t *testing.T) {
	s, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.Seal("same")
	b, _ := s.Seal("same")
	if string(a) == string(b) {
		t.Error("two seals of the same plaintext must differ")
	}
}

func TestKeyPersistsAcrossLoads(t *testing.T) {
	dir := t.TempDir()
	first, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := first.Seal("payload")
	if err != nil {
		t.Fatal(err)
	}

	second, err := Load(dir)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	opened, err := second.Open(sealed)
	if err != nil {
		t.Fatalf("Open with the reloaded key: %v", err)
	}
	if opened != "payload" {
		t.Errorf("Open = %q, want %q", opened, "payload")
	}
}

// The key file guards live credentials; it must not be group or world readable.
func TestKeyFilePermissions(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	dir := t.TempDir()
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, keyFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("secret.key mode = %o, want 600", perm)
	}
}

// A different instance key must not be able to read another's ciphertext.
func TestOpenFailsWithAnotherKey(t *testing.T) {
	mine, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	sealed, err := mine.Seal("payload")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := theirs.Open(sealed); err == nil {
		t.Fatal("a foreign key must not decrypt this ciphertext")
	}
}

func TestOpenRejectsTamperedAndTruncated(t *testing.T) {
	s, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := s.Seal("payload")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("tampered", func(t *testing.T) {
		bad := append([]byte(nil), sealed...)
		bad[len(bad)-1] ^= 0xff
		if _, err := s.Open(bad); err == nil {
			t.Error("GCM must reject a modified ciphertext")
		}
	})

	t.Run("truncated", func(t *testing.T) {
		if _, err := s.Open(sealed[:4]); err == nil {
			t.Error("a value shorter than the nonce must be rejected")
		}
	})
}

func TestKeyFromEnvironment(t *testing.T) {
	// Derived, not hand-written: a miscounted literal fails as a bad key
	// rather than as the behaviour under test.
	encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("A"), keyBytes))
	t.Setenv(KeyEnv, encoded)
	dir := t.TempDir()

	s, err := Load(dir)
	if err != nil {
		t.Fatalf("Load with %s set: %v", KeyEnv, err)
	}
	if _, err := os.Stat(filepath.Join(dir, keyFile)); !os.IsNotExist(err) {
		t.Error("an environment-supplied key must not be written to disk")
	}
	sealed, err := s.Seal("payload")
	if err != nil {
		t.Fatal(err)
	}
	if opened, err := s.Open(sealed); err != nil || opened != "payload" {
		t.Errorf("round trip failed: %q %v", opened, err)
	}
}

func TestRejectsMalformedEnvKey(t *testing.T) {
	for name, value := range map[string]string{
		"not base64":   "!!!not-base64!!!",
		"wrong length": "QUFB",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(KeyEnv, value)
			if _, err := Load(t.TempDir()); err == nil {
				t.Errorf("Load should reject %s", name)
			}
		})
	}
}

// A key derived for a purpose seals and opens bytes bound to their aad,
// deterministically from the instance key, and cannot open what the instance
// key sealed, nor be opened by it.
func TestDerivedKeys(t *testing.T) {
	dir := t.TempDir()
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Derive("recordings")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := d.SealBytes([]byte("a turn"), []byte("row 7"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := d.OpenBytes(sealed, []byte("row 7")); err != nil || string(got) != "a turn" {
		t.Fatalf("round trip = %q, %v", got, err)
	}
	if _, err := d.OpenBytes(sealed, []byte("row 8")); err == nil {
		t.Error("opened with another row's aad")
	}
	if _, err := s.OpenBytes(sealed, []byte("row 7")); err == nil {
		t.Error("the instance key opened what a derived key sealed")
	}
	again, _ := Load(dir)
	d2, _ := again.Derive("recordings")
	if got, err := d2.OpenBytes(sealed, []byte("row 7")); err != nil || string(got) != "a turn" {
		t.Errorf("a key derived again does not open it: %v", err)
	}
	other, _ := s.Derive("something else")
	if _, err := other.OpenBytes(sealed, []byte("row 7")); err == nil {
		t.Error("another purpose's key opened it")
	}

	h, err := s.Hasher("chunks")
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := again.Hasher("chunks")
	h3, _ := s.Hasher("other")
	a, b, c := h([]byte("x")), h2([]byte("x")), h3([]byte("x"))
	if len(a) != 16 || string(a) != string(b) || string(a) == string(c) {
		t.Errorf("hashes: %x %x %x — want 16 bytes, equal for one purpose, different across", a, b, c)
	}
}
