package config

import (
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Origin says where a setting's effective value came from.
//
// Worth reporting because the three sources are not equal and the losing one
// is silent: the file is read first and the environment overrides it, so an
// operator can edit config.yaml, restart, and watch nothing change with no
// error to explain it. That happened — the installed service set
// CLAUDICATION_LISTEN in its unit and shipped no config file at all, and a
// deployment following the documentation got half its settings applied.
type Origin string

const (
	FromDefault Origin = "default"
	FromFile    Origin = "file"
	FromEnv     Origin = "env"
	// FromDatabase marks a value an operator set through the admin UI. It
	// beats all three others and survives a restart, and it is the only one
	// this process can change while it runs — which is exactly why it has to
	// be named on the same screen as the rest. A switch whose provenance is
	// invisible is one an operator flips and then cannot find again.
	FromDatabase Origin = "database"
)

// Setting is one configuration value as it ended up, and how it got there.
type Setting struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Origin Origin `json:"origin"`
	// Env is the variable that overrides this one, empty when none does.
	Env string `json:"env,omitempty"`
	// Doc is a line explaining what the setting does.
	Doc string `json:"doc"`
}

// definitions is the order, the prose and the environment overrides. One
// list, so a setting cannot be added to the loader and quietly go missing from
// the screen that reports it, and an override cannot be named on that screen
// and not be read — applyEnv used to repeat every variable name by hand.
var definitions = []struct {
	key string
	env string
	doc string
	get func(Config) string
	// set applies the environment's value, reporting whether it took it. Nil
	// exactly when env is empty. A value it cannot read is not taken, so the
	// file's value stays in effect and on screen rather than a zero.
	set func(*Config, string) bool
}{
	{"listen", "CLAUDICATION_LISTEN",
		"Where the relay binds.",
		func(c Config) string { return c.Listen },
		func(c *Config, v string) bool { c.Listen = v; return true }},
	{"admin-listen", "CLAUDICATION_ADMIN_LISTEN",
		"Puts the admin API and UI on their own address. Empty shares one listener with the relay, which publishes the admin surface alongside it.",
		func(c Config) string { return c.AdminListen },
		func(c *Config, v string) bool { c.AdminListen = v; return true }},
	{"public-url", "CLAUDICATION_PUBLIC_URL",
		"What clients are told to point at. Needed once the listeners are split, because this page is then on a different address from the relay.",
		func(c Config) string { return c.PublicURL },
		func(c *Config, v string) bool { c.PublicURL = v; return true }},
	{"state-dir", "CLAUDICATION_STATE_DIR",
		"Holds the database and the sealing key. As sensitive as every connected Claude account.",
		func(c Config) string { return c.StateDir },
		func(c *Config, v string) bool { c.StateDir = v; return true }},
	{"trusted-proxies", "CLAUDICATION_TRUSTED_PROXIES",
		"Addresses whose X-Forwarded-For is believed. Empty behind a proxy puts every client in one rate-limit bucket and stops the session cookie being marked Secure.",
		func(c Config) string { return strings.Join(c.TrustedProxies, ", ") },
		setTrustedProxies},

	{"log.level", "CLAUDICATION_LOG_LEVEL", "debug, info, warn or error.",
		func(c Config) string { return c.Log.Level },
		func(c *Config, v string) bool { c.Log.Level = v; return true }},
	{"log.format", "CLAUDICATION_LOG_FORMAT", "json or text.",
		func(c Config) string { return c.Log.Format },
		func(c *Config, v string) bool { c.Log.Format = v; return true }},

	{"limits.requests-per-minute", "CLAUDICATION_REQUESTS_PER_MINUTE",
		"Default per-key allowance. A key with its own limit ignores this.",
		func(c Config) string { return strconv.Itoa(c.Limits.RequestsPerMinute) },
		setRequestsPerMinute},
	{"limits.anon-per-minute", "",
		"Unauthenticated requests per IP, which is also what bounds guessing at the admin password.",
		func(c Config) string { return strconv.Itoa(c.Limits.AnonPerMinute) }, nil},
	{"limits.max-body-bytes", "",
		"Largest request accepted. The body is held in memory so a retry can replay it.",
		func(c Config) string { return strconv.FormatInt(c.Limits.MaxBodyBytes, 10) }, nil},

	{"passthrough.claude-code-attribution", "CLAUDICATION_CLAUDE_CODE_ATTRIBUTION",
		"Adds Claude Code's identity block when a client did not. Off means non-Claude-Code clients reach haiku and nothing above it.",
		func(c Config) string { return strconv.FormatBool(c.Passthrough.ClaudeCodeAttribution) },
		setAttribution},
	{"passthrough.stall-timeout", "",
		"How long a streaming answer may produce nothing before it is sent again, before the client has seen any of it. 0 is off.",
		func(c Config) string { return c.Passthrough.StallTimeout.D().String() }, nil},

	{"usage.retention-days", "",
		"How long per-request history is kept. 0 records nothing at all.",
		func(c Config) string { return strconv.Itoa(c.Usage.RetentionDays) }, nil},
	{"usage.report-days", "",
		"Default reporting window, clamped to retention.",
		func(c Config) string { return strconv.Itoa(c.Usage.ReportDays) }, nil},
	{"usage.poll-idle", "",
		"How often each account's subscription usage is re-read upstream while nobody has this UI open.",
		func(c Config) string { return c.Usage.PollIdle.D().String() }, nil},
	{"usage.poll-watched", "",
		"The same while this UI is open. One upstream call per account per tick, on your own subscriptions; at least 10s.",
		func(c Config) string { return c.Usage.PollWatched.D().String() }, nil},

	{"memory-limit", "",
		"Memory this process may use, e.g. 512MiB. Needed where the container cannot see its own limit; empty detects it where it can.",
		func(c Config) string { return c.MemoryLimit }, nil},
	{"models-catalog", "CLAUDICATION_MODELS_CATALOG",
		"The model catalogue, read every few hours: names, dates and context windows of every model, matched against what the accounts list. Listed as it stands until an account can be asked. off uses the accounts' lists alone.",
		func(c Config) string { return c.ModelsCatalog },
		func(c *Config, v string) bool { c.ModelsCatalog = v; return true }},

	{"shutdown.grace", "",
		"How long in-flight requests may finish after a signal. Streams are long-lived, so this is generous.",
		func(c Config) string { return c.Shutdown.Grace.D().String() }, nil},
}

// Settings reports every value and where it came from, in a fixed order.
func (c Config) Settings() []Setting {
	out := make([]Setting, 0, len(definitions))
	for _, d := range definitions {
		origin := FromDefault
		if c.origins != nil {
			if o, ok := c.origins[d.key]; ok {
				origin = o
			}
		}
		out = append(out, Setting{
			Key:    d.key,
			Value:  d.get(c),
			Origin: origin,
			Env:    d.env,
			Doc:    d.doc,
		})
	}
	return out
}

// recordOrigins works out where each value came from: the environment wins
// over the file, and the file over the defaults.
//
// The file counts when it names the key, not when its value differs from the
// default. An operator who writes the default down has still configured it,
// and reporting that line as "default" sent them looking for why their file
// was not being read.
func recordOrigins(cfg *Config, fromFile, fromEnv map[string]bool) {
	cfg.origins = make(map[string]Origin, len(definitions))
	for _, d := range definitions {
		switch {
		case fromEnv[d.key]:
			cfg.origins[d.key] = FromEnv
		case fromFile[d.key]:
			cfg.origins[d.key] = FromFile
		default:
			cfg.origins[d.key] = FromDefault
		}
	}
}

// fileKeys lists the keys a config document sets, as the dotted paths the
// definitions use. The document has already parsed into a Config, so an
// error here cannot happen in practice and yields no keys.
func fileKeys(data []byte) map[string]bool {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil
	}
	keys := map[string]bool{}
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			keys[prefix+k] = true
			if sub, ok := v.(map[string]any); ok {
				walk(prefix+k+".", sub)
			}
		}
	}
	walk("", doc)
	return keys
}

// setTrustedProxies reads a comma-separated list. Nothing but separators names
// no proxy, so it is not taken: taking it would wipe the file's list and
// report the environment as the reason.
//
// An override at all because the installed service ships no config file and
// this is the setting a proxied deployment most needs. Without one the only
// way to set it is to hand-write a config that nothing told the operator they
// needed — for the one option whose absence silently collapses every client
// into a single rate-limit bucket.
func setTrustedProxies(c *Config, v string) bool {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return false
	}
	c.TrustedProxies = out
	return true
}

// setRequestsPerMinute takes a whole number and ignores anything else, rather
// than reading it as 0.
func setRequestsPerMinute(c *Config, v string) bool {
	n, err := strconv.Atoi(v)
	if err != nil {
		return false
	}
	c.Limits.RequestsPerMinute = n
	return true
}

// setAttribution is on unless the value is 0 or false.
func setAttribution(c *Config, v string) bool {
	c.Passthrough.ClaudeCodeAttribution = v != "0" && !strings.EqualFold(v, "false")
	return true
}
