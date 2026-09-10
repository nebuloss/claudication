// Package config loads claudication's configuration.
//
// Two rules this package exists to enforce, both learned from auth2api:
//
//  1. Configuration is READ-ONLY at runtime. We never write the file back.
//     auth2api re-serialises config.yaml whenever api-keys is empty, which
//     destroys every comment the operator wrote. Credentials live in the
//     state database and are created explicitly, never auto-generated into
//     the config file.
//
//  2. An empty or comment-only file is valid and yields defaults. It must
//     never panic the process before logging is even up.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that decodes from a YAML string like "30s".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("duration must be a string like \"30s\": %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) D() time.Duration { return time.Duration(d) }

type Config struct {
	// Listen is the bind address, e.g. "127.0.0.1:8317".
	Listen string `yaml:"listen"`
	// AdminListen puts the admin API and UI on a second address, leaving
	// Listen serving only the relay and /health.
	//
	// Empty means one listener serves both, which is right on a machine only
	// you can reach and wrong the moment the gateway is exposed: the admin
	// surface can add and remove Claude accounts and mint API keys, so a
	// deployment that publishes /v1 publishes that too unless something in
	// front is configured to stop it. Splitting the listeners makes the
	// separation the gateway's own, not the proxy's — bind this one to
	// localhost or a management interface and it cannot be reached from
	// outside however the proxy is configured.
	AdminListen string `yaml:"admin-listen"`
	// StateDir holds the SQLite database and any other durable state.
	StateDir string `yaml:"state-dir"`
	// TrustedProxies are CIDRs whose X-Forwarded-For we honour. Empty means
	// we never trust the header and always use the socket peer address.
	TrustedProxies []string          `yaml:"trusted-proxies"`
	Log            LogConfig         `yaml:"log"`
	Limits         LimitsConfig      `yaml:"limits"`
	Passthrough    PassthroughConfig `yaml:"passthrough"`
	Usage          UsageConfig       `yaml:"usage"`
	Shutdown       ShutdownConfig    `yaml:"shutdown"`
}

type LogConfig struct {
	Level  string `yaml:"level"`  // debug | info | warn | error
	Format string `yaml:"format"` // json | text
}

type LimitsConfig struct {
	// RequestsPerMinute is the default per-API-key budget. Per-key overrides
	// live in the database. 0 disables key rate limiting.
	RequestsPerMinute int `yaml:"requests-per-minute"`
	// AnonPerMinute limits unauthenticated requests per client IP, so an
	// unauthenticated flood cannot exhaust the process before auth runs.
	AnonPerMinute int `yaml:"anon-per-minute"`
	// MaxBodyBytes caps request bodies. Large-context traffic is legitimately
	// big, so the default is generous but not unbounded.
	MaxBodyBytes int64 `yaml:"max-body-bytes"`
}

type PassthroughConfig struct {
	// ClaudeCodeAttribution prepends Claude Code's attribution block to the
	// system array when the caller did not send one.
	//
	// On by default, because without it the subscription backend refuses opus
	// and sonnet to anything that is not Claude Code — as a 429 that claims to
	// be a rate limit and is not. It is the one place the relay edits a request
	// body, so it is a switch rather than a silent behaviour: turn it off to
	// get strict passthrough and haiku-only for other clients.
	ClaudeCodeAttribution bool `yaml:"claude-code-attribution"`
}

type UsageConfig struct {
	// RetentionDays bounds the per-request history. 0 disables recording
	// entirely, for an operator who would rather the gateway remember nothing.
	RetentionDays int `yaml:"retention-days"`
	// ReportDays is the window the admin UI summarises by default. It cannot
	// usefully exceed retention, and Window clamps it.
	ReportDays int `yaml:"report-days"`
}

// Enabled reports whether per-request usage is recorded at all.
func (u UsageConfig) Enabled() bool { return u.RetentionDays > 0 }

// Retention is how long events are kept.
func (u UsageConfig) Retention() time.Duration {
	return time.Duration(u.RetentionDays) * 24 * time.Hour
}

// Window is the default reporting window, never longer than what is kept —
// a report over 30 days of a 7-day table is a chart with a flat, false tail.
func (u UsageConfig) Window() time.Duration {
	days := u.ReportDays
	if days <= 0 {
		days = 7
	}
	if u.RetentionDays > 0 && days > u.RetentionDays {
		days = u.RetentionDays
	}
	return time.Duration(days) * 24 * time.Hour
}

type ShutdownConfig struct {
	// Grace bounds how long in-flight requests may finish after a signal.
	// Streaming responses are long-lived, so this is generous by design.
	Grace Duration `yaml:"grace"`
}

func Defaults() Config {
	return Config{
		Listen:   "127.0.0.1:8317",
		StateDir: "",
		Log:      LogConfig{Level: "info", Format: "json"},
		Limits: LimitsConfig{
			RequestsPerMinute: 600,
			AnonPerMinute:     60,
			// 32 MiB, not 256. The body is read whole into memory so a retry
			// can replay it, and io.ReadAll on a reader with no size hint —
			// which is what MaxBytesReader gives it — allocates about 2.5× the
			// body while it grows. At 256 MiB a single request could ask for
			// most of a gigabyte on a 512 MB box. 32 MiB is still far above
			// any real Messages request: a long Claude Code transcript is a
			// couple of megabytes.
			MaxBodyBytes: 32 << 20,
		},
		Passthrough: PassthroughConfig{ClaudeCodeAttribution: true},
		Usage:       UsageConfig{RetentionDays: 30, ReportDays: 7},
		Shutdown:    ShutdownConfig{Grace: Duration(120 * time.Second)},
	}
}

// Load reads path (may be empty), applies environment overrides, resolves the
// state directory and validates the result. It never writes to disk.
func Load(path string) (Config, error) {
	cfg := Defaults()

	if path != "" {
		data, err := os.ReadFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return Config{}, fmt.Errorf("config file %s does not exist", path)
		case err != nil:
			return Config{}, fmt.Errorf("read config %s: %w", path, err)
		}
		// An empty or comment-only document leaves cfg at its defaults;
		// yaml.Unmarshal reports no error and touches nothing.
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config %s: %w", path, err)
		}
	}

	applyEnv(&cfg)

	if cfg.StateDir == "" {
		dir, err := defaultStateDir()
		if err != nil {
			return Config{}, err
		}
		cfg.StateDir = dir
	}
	abs, err := filepath.Abs(expandHome(cfg.StateDir))
	if err != nil {
		return Config{}, fmt.Errorf("resolve state-dir: %w", err)
	}
	cfg.StateDir = abs

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("CLAUDICATION_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("CLAUDICATION_ADMIN_LISTEN"); v != "" {
		cfg.AdminListen = v
	}
	if v := os.Getenv("CLAUDICATION_STATE_DIR"); v != "" {
		cfg.StateDir = v
	}
	if v := os.Getenv("CLAUDICATION_LOG_LEVEL"); v != "" {
		cfg.Log.Level = v
	}
	if v := os.Getenv("CLAUDICATION_LOG_FORMAT"); v != "" {
		cfg.Log.Format = v
	}
	if v := os.Getenv("CLAUDICATION_CLAUDE_CODE_ATTRIBUTION"); v != "" {
		cfg.Passthrough.ClaudeCodeAttribution = v != "0" && !strings.EqualFold(v, "false")
	}
	if v := os.Getenv("CLAUDICATION_REQUESTS_PER_MINUTE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Limits.RequestsPerMinute = n
		}
	}
	// Comma-separated, because the installed service ships no config file and
	// this is the setting a proxied deployment most needs. Without an override
	// the only way to set it is to hand-write a config that nothing told the
	// operator they needed — for the one option whose absence silently collapses
	// every client into a single rate-limit bucket.
	if v := os.Getenv("CLAUDICATION_TRUSTED_PROXIES"); v != "" {
		var out []string
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		cfg.TrustedProxies = out
	}
}

func (c Config) validate() error {
	if c.Listen == "" {
		return errors.New("listen must not be empty")
	}
	// Same address on both would mean whichever bound first wins and the other
	// fails with "address already in use", which is a confusing way to learn
	// the admin surface is not split at all.
	if c.AdminListen != "" && c.AdminListen == c.Listen {
		return errors.New("admin-listen must differ from listen, or be empty to share one listener")
	}
	if c.Limits.MaxBodyBytes <= 0 {
		return errors.New("limits.max-body-bytes must be positive")
	}
	if c.Shutdown.Grace.D() <= 0 {
		return errors.New("shutdown.grace must be positive")
	}
	for _, p := range c.TrustedProxies {
		if strings.TrimSpace(p) == "" {
			return errors.New("trusted-proxies contains an empty entry")
		}
	}
	return nil
}

// DBPath is the SQLite file inside the state directory.
func (c Config) DBPath() string { return filepath.Join(c.StateDir, "claudication.db") }

// EnsureStateDir creates the state directory and proves it is writable.
//
// This is the startup check auth2api lacks: its Docker compose mounts a volume
// at /data while auth-dir defaults to ~/.auth2api, so tokens silently land on
// the container's writable layer and vanish on recreate. Failing loudly here
// turns that class of misconfiguration into a startup error.
func (c Config) EnsureStateDir() error {
	if err := os.MkdirAll(c.StateDir, 0o700); err != nil {
		return fmt.Errorf("create state-dir %s: %w", c.StateDir, err)
	}
	probe := filepath.Join(c.StateDir, ".write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("state-dir %s is not writable: %w", c.StateDir, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("state-dir %s is not writable: %w", c.StateDir, err)
	}
	if err := os.Remove(probe); err != nil {
		return fmt.Errorf("state-dir %s is not writable: %w", c.StateDir, err)
	}
	return nil
}

func defaultStateDir() (string, error) {
	// Containers get an explicit, predictable path; anything else follows XDG.
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "/var/lib/claudication", nil
	}
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "claudication"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for state-dir: %w", err)
	}
	return filepath.Join(home, ".local", "state", "claudication"), nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}
