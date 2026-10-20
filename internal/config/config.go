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
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"claudication/internal/memlimit"
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
	// PublicURL is the base URL clients should point at, when that is not the
	// address the admin UI is reached on.
	//
	// The UI shows a client the origin its own browser used, which is right
	// while one listener serves everything and wrong the moment AdminListen
	// splits them: the browser is talking to the admin surface, and the relay
	// is somewhere else entirely — often a different hostname on a different
	// proxy. Nothing the gateway can see tells it that name, so an operator
	// who splits the listeners has to say it here or the UI hands out
	// instructions that 404.
	PublicURL string `yaml:"public-url"`
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
	// MemoryLimit is the memory this process may use, like "512MiB": the
	// container's limit, on a host where the container cannot see it — a
	// Proxmox LXC among them. Empty detects it from the cgroup where that is
	// visible, and otherwise sets none. See internal/memlimit.
	MemoryLimit string `yaml:"memory-limit"`
	// ModelsCatalog is where the model catalogue is read from — models.dev by
	// default: every provider's models, which the gateway's model list is
	// matched from against what the accounts serve. See catalog.go in
	// internal/service/models. "off" or empty never asks; the list is the accounts' own.
	ModelsCatalog string `yaml:"models-catalog"`

	// Path is the file this was read from, empty when there was none. Reported
	// by the admin UI so "where do I change this" has an answer on screen.
	Path string `yaml:"-"`
	// origins records where each setting's effective value came from. Set by
	// Load; unexported so a config file cannot claim its own provenance.
	origins map[string]Origin
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
	// StallTimeout is how long a streaming answer may produce nothing before
	// the attempt is abandoned and the request sent again, invisibly to the
	// client. 0 turns it off. See internal/relay/stall.go for what was
	// measured: accepted streams that sent message_start and then nothing for
	// five minutes, while the requests beside them ran normally.
	StallTimeout Duration `yaml:"stall-timeout"`
}

// MinStallTimeout keeps the stall check clear of an ordinary slow start: a
// long prompt can legitimately take tens of seconds to its first token.
const MinStallTimeout = 30 * time.Second

type UsageConfig struct {
	// RetentionDays bounds the per-request history. 0 disables recording
	// entirely, for an operator who would rather the gateway remember nothing.
	RetentionDays int `yaml:"retention-days"`
	// ReportDays is the window the admin UI summarises by default. It cannot
	// usefully exceed retention, and Window clamps it.
	ReportDays int `yaml:"report-days"`
	// PollIdle is how often every account's subscription usage is re-read
	// upstream while nobody has the admin UI open. The figures only need to
	// be honest by the time someone looks, so this is slow.
	PollIdle Duration `yaml:"poll-idle"`
	// PollWatched is the same while the admin UI is open, when the figures
	// are the screen. It is one GET /api/oauth/usage per account per tick,
	// sent on the operator's own subscriptions, and nobody has measured where
	// that endpoint starts refusing — so it is a setting rather than a
	// constant, and has a floor.
	PollWatched Duration `yaml:"poll-watched"`
}

// MinUsagePoll is the fastest either poll may run. Below it the upstream is
// being asked faster than its own figures move.
const MinUsagePoll = 10 * time.Second

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
		Passthrough: PassthroughConfig{
			ClaudeCodeAttribution: true,
			// Under the five minutes clients wait before giving up on a silent
			// stream, with room for the retry to answer inside them.
			StallTimeout: Duration(90 * time.Second),
		},
		Usage: UsageConfig{
			RetentionDays: 30,
			ReportDays:    7,
			PollIdle:      Duration(5 * time.Minute),
			PollWatched:   Duration(20 * time.Second),
		},
		Shutdown:      ShutdownConfig{Grace: Duration(120 * time.Second)},
		ModelsCatalog: "https://models.dev/api.json",
	}
}

// Load reads path (may be empty), applies environment overrides, resolves the
// state directory and validates the result. It never writes to disk.
func Load(path string) (Config, error) {
	cfg := Defaults()
	var fromFile map[string]bool

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
		fromFile = fileKeys(data)
	}

	fromEnv := applyEnv(&cfg)
	cfg.Path = path

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
	// Last, so state-dir's resolved absolute path is what gets compared and a
	// directory that only the default supplied does not read as configured.
	recordOrigins(&cfg, fromFile, fromEnv)
	return cfg, nil
}

// applyEnv overrides from the environment and reports which settings it
// took over, so Settings can say so rather than leaving the file's value on
// screen next to a different one in effect.
func applyEnv(cfg *Config) map[string]bool {
	took := map[string]bool{}
	for _, d := range definitions {
		if d.env == "" {
			continue
		}
		if v := os.Getenv(d.env); v != "" && d.set(cfg, v) {
			took[d.key] = true
		}
	}
	return took
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
	// Checked rather than read leniently: the logger falls back to info and
	// json for anything it does not know, so a typo here used to mean a
	// gateway quietly logging at a level nobody chose.
	switch strings.ToLower(c.Log.Level) {
	case "", "debug", "info", "warn", "warning", "error":
	default:
		return fmt.Errorf("log.level must be debug, info, warn or error, not %q", c.Log.Level)
	}
	switch strings.ToLower(c.Log.Format) {
	case "", "json", "text":
	default:
		return fmt.Errorf("log.format must be json or text, not %q", c.Log.Format)
	}
	if c.Limits.MaxBodyBytes <= 0 {
		return errors.New("limits.max-body-bytes must be positive")
	}
	if c.Usage.PollIdle.D() < MinUsagePoll || c.Usage.PollWatched.D() < MinUsagePoll {
		return fmt.Errorf("usage.poll-idle and usage.poll-watched must be at least %s", MinUsagePoll)
	}
	if c.Usage.PollWatched.D() > c.Usage.PollIdle.D() {
		return errors.New("usage.poll-watched must not be slower than usage.poll-idle")
	}
	if n, err := memlimit.ParseSize(c.MemoryLimit); err != nil {
		return fmt.Errorf("memory-limit: %w", err)
	} else if n != 0 && n < 64<<20 {
		return errors.New("memory-limit below 64MiB leaves nothing to serve a request with")
	}
	if u := c.ModelsCatalog; u != "" && u != "off" &&
		!strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return errors.New(`models-catalog must be an http(s) URL, or "off"`)
	}
	if st := c.Passthrough.StallTimeout.D(); st != 0 && st < MinStallTimeout {
		return fmt.Errorf("passthrough.stall-timeout must be 0 (off) or at least %s", MinStallTimeout)
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
