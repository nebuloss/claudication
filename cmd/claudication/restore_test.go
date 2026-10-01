package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeArchive builds a gzipped tar of the given entries, the shape restore
// reads, so a test can hand it something a backup would never contain.
func writeArchive(t *testing.T, entries map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "crafted.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// A backup is only worth having if it restores into a gateway that can read
// what it held — the database and the sealing key together. Checked by
// authenticating the same client key on the other side.
func TestBackupRestoresIntoAWorkingGateway(t *testing.T) {
	_, cfg := stateDir(t)
	out, err := captureStdout(t, func() error {
		return cmdKeys([]string{"add", "-config", cfg, "-name", "travels"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var plaintext string
	for _, f := range strings.Fields(out) {
		if strings.HasPrefix(f, "clc_") {
			plaintext = f
		}
	}

	archive := filepath.Join(t.TempDir(), "b.tar.gz")
	out, err = captureStdout(t, func() error { return cmdBackup([]string{"-config", cfg, archive}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Wrote "+archive) {
		t.Errorf("backup said %q", out)
	}
	info, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	// It holds the sealing key: nobody else may read it, from the first byte.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("backup mode = %o, want 600", perm)
	}

	// Never overwrite: a second backup to the same name is refused.
	if err := cmdBackup([]string{"-config", cfg, "-out", archive}); err == nil {
		t.Error("backup overwrote an existing file")
	}

	// Into a fresh state directory.
	dest, cfg2 := stateDir(t)
	out, err = captureStdout(t, func() error { return cmdRestore([]string{"-config", cfg2, archive}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Restored into "+dest) || strings.Contains(out, "Warning") {
		t.Errorf("restore said %q", out)
	}
	key, err := openStore(t, dest).Authenticate(context.Background(), plaintext)
	if err != nil || key.Name != "travels" {
		t.Fatalf("restored gateway does not know the key: %+v, %v", key, err)
	}

	// Over an existing gateway only when told to.
	if err := cmdRestore([]string{"-config", cfg2, "-in", archive}); err == nil ||
		!strings.Contains(err.Error(), "-force") {
		t.Errorf("restore over existing state: err = %v, want a pointer to -force", err)
	}
	if _, err := captureStdout(t, func() error {
		return cmdRestore([]string{"-config", cfg2, "-force", archive})
	}); err != nil {
		t.Errorf("restore -force: %v", err)
	}
}

// With no destination given the backup still has to land somewhere named for
// when it was taken, in the working directory.
func TestBackupDefaultsToADatedName(t *testing.T) {
	_, cfg := stateDir(t)
	wd := t.TempDir()
	t.Chdir(wd)
	if _, err := captureStdout(t, func() error { return cmdBackup([]string{"-config", cfg}) }); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(wd, "claudication-backup-*.tar.gz"))
	if len(matches) != 1 {
		t.Errorf("found %v, want one dated backup", matches)
	}
}

// The archive is attacker-shaped input. A name with .. in it must land in the
// state directory under its base name, or not at all — never wherever it
// points.
func TestRestoreDoesNotFollowPathsOutOfTheStateDir(t *testing.T) {
	dest, cfg := stateDir(t)
	outside := filepath.Join(t.TempDir(), "escaped")
	archive := writeArchive(t, map[string]string{
		"../../../../claudication.db": "db-bytes",
		"secret.key":                  "key-bytes",
		outside:                       "should not be written",
	})
	if _, err := captureStdout(t, func() error { return cmdRestore([]string{"-config", cfg, archive}) }); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "claudication.db")); string(got) != "db-bytes" {
		t.Errorf("db = %q, want it written inside the state dir", got)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Error("an entry outside the two known names was written")
	}
}

// A backup missing the key restores accounts whose tokens cannot be opened;
// that has to be said now, not discovered at the first request.
func TestRestoreWarnsWithoutTheKey(t *testing.T) {
	_, cfg := stateDir(t)
	archive := writeArchive(t, map[string]string{"claudication.db": "x"})
	out, err := captureStdout(t, func() error { return cmdRestore([]string{"-config", cfg, archive}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no secret.key") {
		t.Errorf("restore without a key said %q", out)
	}
}

// Anything that is not a backup is refused rather than half-applied.
func TestRestoreRefusesWhatIsNotABackup(t *testing.T) {
	_, cfg := stateDir(t)
	if err := cmdRestore([]string{"-config", cfg}); err == nil {
		t.Error("no file: want an error")
	}
	if err := cmdRestore([]string{"-config", cfg, "/nonexistent.tar.gz"}); err == nil {
		t.Error("missing file: want an error")
	}
	plain := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(plain, []byte("not gzip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdRestore([]string{"-config", cfg, plain}); err == nil {
		t.Error("a non-gzip file restored")
	}
	other := writeArchive(t, map[string]string{"notes.txt": "hello"})
	if err := cmdRestore([]string{"-config", cfg, other}); err == nil ||
		!strings.Contains(err.Error(), "claudication.db") {
		t.Errorf("an archive with no database: err = %v", err)
	}
	if err := cmdRestore([]string{"-config", cfg, "a", "b"}); err == nil {
		t.Error("two files: want an error")
	}
	if err := cmdRestore([]string{"-config", "/nonexistent.yaml", "x"}); err == nil {
		t.Error("missing config: want an error")
	}
	if err := cmdRestore([]string{"-zzz"}); err == nil {
		t.Error("unknown flag: want an error")
	}
	if err := cmdBackup([]string{"-zzz"}); err == nil {
		t.Error("backup unknown flag: want an error")
	}
	if err := cmdBackup([]string{"a", "b"}); err == nil {
		t.Error("backup two files: want an error")
	}
	if err := cmdBackup([]string{"-config", "/nonexistent.yaml"}); err == nil {
		t.Error("backup missing config: want an error")
	}
}
