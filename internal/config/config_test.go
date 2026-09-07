package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// An empty or comment-only config must yield defaults, not a crash. auth2api
// dereferences the parse result unconditionally and dies with a TypeError.
func TestLoadEmptyAndCommentOnlyFile(t *testing.T) {
	for name, body := range map[string]string{
		"empty":        "",
		"comment only": "# nothing here\n",
		"just newline": "\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Listen != Defaults().Listen {
				t.Errorf("Listen = %q, want default %q", cfg.Listen, Defaults().Listen)
			}
		})
	}
}

// Loading must never modify the file on disk.
func TestLoadDoesNotWriteConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := "# keep this comment\nlisten: \"127.0.0.1:9999\"\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != original {
		t.Errorf("config file was rewritten:\n got: %q\nwant: %q", after, original)
	}
}

func TestLoadParsesDurationsAndOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "listen: \"0.0.0.0:1234\"\nshutdown:\n  grace: \"45s\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != "0.0.0.0:1234" {
		t.Errorf("Listen = %q", cfg.Listen)
	}
	if cfg.Shutdown.Grace.D() != 45*time.Second {
		t.Errorf("Grace = %v, want 45s", cfg.Shutdown.Grace.D())
	}

	t.Setenv("CLAUDICATION_LISTEN", "127.0.0.1:4321")
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != "127.0.0.1:4321" {
		t.Errorf("env override ignored: Listen = %q", cfg.Listen)
	}
}

func TestLoadMissingFileIsAnError(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("expected an error for a missing config file")
	}
}

func TestEnsureStateDirRejectsUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	base := t.TempDir()
	locked := filepath.Join(base, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	cfg := Defaults()
	cfg.StateDir = filepath.Join(locked, "state")
	if err := cfg.EnsureStateDir(); err == nil {
		t.Fatal("expected an error for an unwritable state dir")
	}
}
