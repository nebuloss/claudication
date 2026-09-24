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

// The fast usage poll is traffic on the operator's own subscriptions, so the
// floor is not negotiable and a watched rate slower than the idle one is a
// typo rather than a choice.
func TestUsagePollBounds(t *testing.T) {
	for body, ok := range map[string]bool{
		"": true,
		"usage:\n  poll-watched: \"15s\"\n  poll-idle: \"10m\"\n": true,
		"usage:\n  poll-watched: \"2s\"\n":                        false,
		"usage:\n  poll-idle: \"5s\"\n":                           false,
		"usage:\n  poll-watched: \"10m\"\n":                       false,
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if (err == nil) != ok {
			t.Errorf("%q: Load error = %v, want ok=%v", body, err, ok)
		}
	}
}

// The annotated example is what operators copy, so it has to load, and every
// key it sets has to land where it says. It once carried retention-days at
// column zero, outside usage:, and the copy failed to parse.
func TestExampleConfigLoads(t *testing.T) {
	t.Setenv("CLAUDICATION_STATE_DIR", t.TempDir())
	cfg, err := Load(filepath.Join("..", "..", "configs", "config.example.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Usage.RetentionDays != 30 || cfg.Usage.PollWatched.D() != 20*time.Second {
		t.Errorf("usage section not read: %+v", cfg.Usage)
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
