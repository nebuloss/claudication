package config

import (
	"strconv"
	"strings"
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

// definitions is the order and the prose. One list, so a setting cannot be
// added to the loader and quietly go missing from the screen that reports it.
var definitions = []struct {
	key string
	env string
	doc string
	get func(Config) string
}{
	{"listen", "CLAUDICATION_LISTEN",
		"Where the relay binds.",
		func(c Config) string { return c.Listen }},
	{"admin-listen", "CLAUDICATION_ADMIN_LISTEN",
		"Puts the admin API and UI on their own address. Empty shares one listener with the relay, which publishes the admin surface alongside it.",
		func(c Config) string { return c.AdminListen }},
	{"public-url", "CLAUDICATION_PUBLIC_URL",
		"What clients are told to point at. Needed once the listeners are split, because this page is then on a different address from the relay.",
		func(c Config) string { return c.PublicURL }},
	{"state-dir", "CLAUDICATION_STATE_DIR",
		"Holds the database and the sealing key. As sensitive as every connected Claude account.",
		func(c Config) string { return c.StateDir }},
	{"trusted-proxies", "CLAUDICATION_TRUSTED_PROXIES",
		"Addresses whose X-Forwarded-For is believed. Empty behind a proxy puts every client in one rate-limit bucket and stops the session cookie being marked Secure.",
		func(c Config) string { return strings.Join(c.TrustedProxies, ", ") }},

	{"log.level", "CLAUDICATION_LOG_LEVEL", "debug, info, warn or error.",
		func(c Config) string { return c.Log.Level }},
	{"log.format", "CLAUDICATION_LOG_FORMAT", "json or text.",
		func(c Config) string { return c.Log.Format }},

	{"limits.requests-per-minute", "CLAUDICATION_REQUESTS_PER_MINUTE",
		"Default per-key allowance. A key with its own limit ignores this.",
		func(c Config) string { return strconv.Itoa(c.Limits.RequestsPerMinute) }},
	{"limits.anon-per-minute", "",
		"Unauthenticated requests per IP, which is also what bounds guessing at the admin password.",
		func(c Config) string { return strconv.Itoa(c.Limits.AnonPerMinute) }},
	{"limits.max-body-bytes", "",
		"Largest request accepted. The body is held in memory so a retry can replay it.",
		func(c Config) string { return strconv.FormatInt(c.Limits.MaxBodyBytes, 10) }},

	{"passthrough.claude-code-attribution", "CLAUDICATION_CLAUDE_CODE_ATTRIBUTION",
		"Adds Claude Code's identity block when a client did not. Off means non-Claude-Code clients reach haiku and nothing above it.",
		func(c Config) string { return strconv.FormatBool(c.Passthrough.ClaudeCodeAttribution) }},

	{"openai.model", "CLAUDICATION_OPENAI_MODEL",
		"Which Claude model a Responses request runs on when it asks for something else — a default Codex install asks for gpt-5-codex. A request naming a Claude model keeps it.",
		func(c Config) string { return c.OpenAI.Model }},
	{"openai.max-tokens", "CLAUDICATION_OPENAI_MAX_TOKENS",
		"Answer ceiling for a Responses request, which never carries one of its own. Anthropic requires the field, so there has to be a figure here.",
		func(c Config) string { return strconv.Itoa(c.OpenAI.MaxTokens) }},

	{"usage.retention-days", "",
		"How long per-request history is kept. 0 records nothing at all.",
		func(c Config) string { return strconv.Itoa(c.Usage.RetentionDays) }},
	{"usage.report-days", "",
		"Default reporting window, clamped to retention.",
		func(c Config) string { return strconv.Itoa(c.Usage.ReportDays) }},

	{"shutdown.grace", "",
		"How long in-flight requests may finish after a signal. Streams are long-lived, so this is generous.",
		func(c Config) string { return c.Shutdown.Grace.D().String() }},
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

// recordOrigins works out where each value came from by comparing the three
// stages: what the defaults said, what the file changed, and what the
// environment then took over.
func recordOrigins(cfg *Config, defaults, afterFile Config, fromEnv map[string]bool) {
	cfg.origins = make(map[string]Origin, len(definitions))
	for _, d := range definitions {
		switch {
		case fromEnv[d.key]:
			cfg.origins[d.key] = FromEnv
		case d.get(afterFile) != d.get(defaults):
			cfg.origins[d.key] = FromFile
		default:
			cfg.origins[d.key] = FromDefault
		}
	}
}
