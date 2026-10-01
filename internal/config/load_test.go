package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// envVars is every variable Load reads. Tests clear them all first so the
// environment the suite happens to run in cannot change the outcome.
var envVars = []string{
	"CLAUDICATION_LISTEN", "CLAUDICATION_ADMIN_LISTEN", "CLAUDICATION_DOCS_LISTEN",
	"CLAUDICATION_DOCS_URL", "CLAUDICATION_PUBLIC_URL", "CLAUDICATION_STATE_DIR",
	"CLAUDICATION_LOG_LEVEL", "CLAUDICATION_LOG_FORMAT",
	"CLAUDICATION_CLAUDE_CODE_ATTRIBUTION", "CLAUDICATION_OPENAI_MODEL",
	"CLAUDICATION_OPENAI_MAX_TOKENS", "CLAUDICATION_REQUESTS_PER_MINUTE",
	"CLAUDICATION_TRUSTED_PROXIES",
}

func cleanEnv(t *testing.T) {
	t.Helper()
	for _, v := range envVars {
		t.Setenv(v, "")
	}
}

// loadBody writes body as a config file and loads it, with the state directory
// pinned so nothing depends on the user's home.
func loadBody(t *testing.T, body string) (Config, error) {
	t.Helper()
	cleanEnv(t)
	t.Setenv("CLAUDICATION_STATE_DIR", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

// The installed service ships no config file, so the environment is the only
// way most deployments configure anything. Every override has to land.
func TestEveryEnvironmentOverride(t *testing.T) {
	cleanEnv(t)
	state := t.TempDir()
	for k, v := range map[string]string{
		"CLAUDICATION_LISTEN":                  "0.0.0.0:1",
		"CLAUDICATION_ADMIN_LISTEN":            "127.0.0.1:2",
		"CLAUDICATION_DOCS_LISTEN":             "127.0.0.1:3",
		"CLAUDICATION_DOCS_URL":                "https://docs.example",
		"CLAUDICATION_PUBLIC_URL":              "https://relay.example",
		"CLAUDICATION_STATE_DIR":               state,
		"CLAUDICATION_LOG_LEVEL":               "debug",
		"CLAUDICATION_LOG_FORMAT":              "text",
		"CLAUDICATION_CLAUDE_CODE_ATTRIBUTION": "false",
		"CLAUDICATION_OPENAI_MODEL":            "claude-opus-5",
		"CLAUDICATION_OPENAI_MAX_TOKENS":       "1234",
		"CLAUDICATION_REQUESTS_PER_MINUTE":     "7",
		"CLAUDICATION_TRUSTED_PROXIES":         " 10.0.0.0/8, ,192.168.0.0/16 ,",
	} {
		t.Setenv(k, v)
	}

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	checks := map[string][2]any{
		"listen":       {cfg.Listen, "0.0.0.0:1"},
		"admin-listen": {cfg.AdminListen, "127.0.0.1:2"},
		"docs-listen":  {cfg.DocsListen, "127.0.0.1:3"},
		"docs-url":     {cfg.DocsURL, "https://docs.example"},
		"public-url":   {cfg.PublicURL, "https://relay.example"},
		"state-dir":    {cfg.StateDir, state},
		"log.level":    {cfg.Log.Level, "debug"},
		"log.format":   {cfg.Log.Format, "text"},
		"attribution":  {cfg.Passthrough.ClaudeCodeAttribution, false},
		"openai.model": {cfg.OpenAI.Model, "claude-opus-5"},
		"max-tokens":   {cfg.OpenAI.MaxTokens, 1234},
		"rpm":          {cfg.Limits.RequestsPerMinute, 7},
		// Blank entries between commas are dropped, not kept as an empty
		// CIDR that validate would then refuse.
		"proxies": {cfg.TrustedProxies, []string{"10.0.0.0/8", "192.168.0.0/16"}},
	}
	for name, c := range checks {
		if !reflect.DeepEqual(c[0], c[1]) {
			t.Errorf("%s = %#v, want %#v", name, c[0], c[1])
		}
	}
	for _, s := range cfg.Settings() {
		if s.Env != "" && s.Origin != FromEnv {
			t.Errorf("%s was set by %s but reports origin %q", s.Key, s.Env, s.Origin)
		}
	}
}

// Attribution is the one switch that is on by default, so only an explicit
// "off" may turn it off: anything else an operator types means "on".
func TestAttributionEnvValues(t *testing.T) {
	for v, want := range map[string]bool{
		"0": false, "false": false, "FALSE": false, "False": false,
		"1": true, "true": true, "yes": true, "off": true,
	} {
		cleanEnv(t)
		t.Setenv("CLAUDICATION_STATE_DIR", t.TempDir())
		t.Setenv("CLAUDICATION_CLAUDE_CODE_ATTRIBUTION", v)
		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Passthrough.ClaudeCodeAttribution != want {
			t.Errorf("CLAUDICATION_CLAUDE_CODE_ATTRIBUTION=%q gave %v, want %v", v, cfg.Passthrough.ClaudeCodeAttribution, want)
		}
	}
}

// A number that does not parse is ignored and the previous value stands, and
// the setting must not then claim the environment set it.
func TestUnparseableNumericEnvIsIgnored(t *testing.T) {
	cleanEnv(t)
	t.Setenv("CLAUDICATION_STATE_DIR", t.TempDir())
	t.Setenv("CLAUDICATION_OPENAI_MAX_TOKENS", "lots")
	t.Setenv("CLAUDICATION_REQUESTS_PER_MINUTE", "many")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	d := Defaults()
	if cfg.OpenAI.MaxTokens != d.OpenAI.MaxTokens || cfg.Limits.RequestsPerMinute != d.Limits.RequestsPerMinute {
		t.Errorf("unparseable numbers changed the config: %d, %d", cfg.OpenAI.MaxTokens, cfg.Limits.RequestsPerMinute)
	}
	for _, s := range cfg.Settings() {
		if (s.Key == "openai.max-tokens" || s.Key == "limits.requests-per-minute") && s.Origin != FromDefault {
			t.Errorf("%s reports origin %q for an ignored override", s.Key, s.Origin)
		}
	}
}

// Every refusal validate can produce. Each is a configuration that would start
// and then misbehave — or publish what should be private — so it has to fail
// at load, with a message naming the setting.
func TestValidationErrors(t *testing.T) {
	cases := map[string]struct{ body, mention string }{
		"empty listen":        {"listen: \"\"\n", "listen"},
		"admin same as relay": {"listen: \"a:1\"\nadmin-listen: \"a:1\"\n", "admin-listen"},
		"docs same as relay":  {"listen: \"a:1\"\ndocs-listen: \"a:1\"\n", "docs-listen"},
		"docs same as admin":  {"admin-listen: \"a:2\"\ndocs-listen: \"a:2\"\n", "docs-listen"},
		"zero body cap":       {"limits:\n  max-body-bytes: 0\n", "max-body-bytes"},
		"zero max tokens":     {"openai:\n  max-tokens: 0\n", "max-tokens"},
		"bad memory limit":    {"memory-limit: \"lots\"\n", "memory-limit"},
		"tiny memory limit":   {"memory-limit: \"32MiB\"\n", "memory-limit"},
		"short stall timeout": {"passthrough:\n  stall-timeout: \"5s\"\n", "stall-timeout"},
		"zero grace":          {"shutdown:\n  grace: \"0s\"\n", "grace"},
		"blank proxy entry":   {"trusted-proxies: [\"10.0.0.0/8\", \" \"]\n", "trusted-proxies"},
		"bad yaml":            {"listen: [\n", "parse"},
		"duration not string": {"shutdown:\n  grace: {a: 1}\n", "duration"},
		"duration nonsense":   {"shutdown:\n  grace: \"soon\"\n", "invalid duration"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadBody(t, c.body)
			if err == nil {
				t.Fatalf("%q loaded without error", c.body)
			}
			if !strings.Contains(err.Error(), c.mention) {
				t.Errorf("error %q does not mention %q", err, c.mention)
			}
		})
	}
}

// The boundaries are part of the contract: 0 turns the stall check off, the
// floor itself is allowed, and the smallest memory limit is accepted.
func TestValidationAcceptsTheBoundaries(t *testing.T) {
	for _, body := range []string{
		"passthrough:\n  stall-timeout: \"0s\"\n",
		"passthrough:\n  stall-timeout: \"30s\"\n",
		"memory-limit: \"64MiB\"\n",
		"memory-limit: \"\"\n",
		"listen: \"a:1\"\nadmin-listen: \"a:2\"\ndocs-listen: \"a:3\"\n",
	} {
		if _, err := loadBody(t, body); err != nil {
			t.Errorf("%q refused: %v", body, err)
		}
	}
}

// A config path that exists but is not a readable file is a read error, not
// the "does not exist" message that would send the operator looking in the
// wrong place.
func TestLoadUnreadablePath(t *testing.T) {
	cleanEnv(t)
	_, err := Load(t.TempDir())
	if err == nil || strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("Load(directory) = %v, want a read error", err)
	}
}

// Origins are what lets the settings screen explain why an edit to the file
// did nothing: each value has to name the stage that actually set it.
func TestSettingsOrigins(t *testing.T) {
	cleanEnv(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "listen: \"127.0.0.1:1111\"\nlog:\n  level: debug\nlimits:\n  anon-per-minute: 5\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDICATION_LISTEN", "127.0.0.1:2222")
	t.Setenv("CLAUDICATION_STATE_DIR", t.TempDir())

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Path != path {
		t.Errorf("Path = %q, want %q", cfg.Path, path)
	}
	got := map[string]Setting{}
	for _, s := range cfg.Settings() {
		got[s.Key] = s
	}
	for key, want := range map[string]struct {
		origin Origin
		value  string
	}{
		"listen":                 {FromEnv, "127.0.0.1:2222"},
		"log.level":              {FromFile, "debug"},
		"limits.anon-per-minute": {FromFile, "5"},
		"log.format":             {FromDefault, "json"},
		"state-dir":              {FromEnv, cfg.StateDir},
		"shutdown.grace":         {FromDefault, "2m0s"},
		"usage.poll-watched":     {FromDefault, "20s"},
	} {
		s := got[key]
		if s.Origin != want.origin || s.Value != want.value {
			t.Errorf("%s = %q from %q, want %q from %q", key, s.Value, s.Origin, want.value, want.origin)
		}
	}
	if got["listen"].Env != "CLAUDICATION_LISTEN" || got["limits.anon-per-minute"].Env != "" {
		t.Error("the overriding variable is not reported correctly")
	}
}

// Settings is one list in a fixed order; a setting reported twice or with no
// explanation is a screen an operator cannot trust. A Config built by hand
// rather than loaded has no origins, which reads as all defaults.
func TestSettingsIsCompleteAndOrdered(t *testing.T) {
	settings := Defaults().Settings()
	if len(settings) != len(definitions) {
		t.Fatalf("%d settings for %d definitions", len(settings), len(definitions))
	}
	seen := map[string]bool{}
	for i, s := range settings {
		if s.Key != definitions[i].key {
			t.Errorf("setting %d is %s, want %s", i, s.Key, definitions[i].key)
		}
		if seen[s.Key] {
			t.Errorf("%s reported twice", s.Key)
		}
		seen[s.Key] = true
		if s.Doc == "" {
			t.Errorf("%s has no explanation", s.Key)
		}
		if s.Origin != FromDefault {
			t.Errorf("%s on an unloaded config reports %q", s.Key, s.Origin)
		}
	}
}

// Retention 0 means "remember nothing", and the report window can never claim
// more history than is kept — a 30-day chart over a 7-day table has a flat,
// false tail.
func TestUsageWindowAndRetention(t *testing.T) {
	day := 24 * time.Hour
	for _, c := range []struct {
		u       UsageConfig
		enabled bool
		keep    time.Duration
		window  time.Duration
	}{
		{UsageConfig{RetentionDays: 30, ReportDays: 7}, true, 30 * day, 7 * day},
		{UsageConfig{RetentionDays: 3, ReportDays: 7}, true, 3 * day, 3 * day},
		{UsageConfig{RetentionDays: 30, ReportDays: 0}, true, 30 * day, 7 * day},
		{UsageConfig{RetentionDays: 0, ReportDays: 14}, false, 0, 14 * day},
	} {
		if c.u.Enabled() != c.enabled || c.u.Retention() != c.keep || c.u.Window() != c.window {
			t.Errorf("%+v: enabled %v keep %v window %v; want %v %v %v",
				c.u, c.u.Enabled(), c.u.Retention(), c.u.Window(), c.enabled, c.keep, c.window)
		}
	}
}

// The database lives inside the state directory, never beside it, so one
// directory is the whole of what must be backed up and protected.
func TestDBPath(t *testing.T) {
	c := Config{StateDir: "/var/lib/claudication"}
	if got := c.DBPath(); got != "/var/lib/claudication/claudication.db" {
		t.Errorf("DBPath = %q", got)
	}
}

// The happy path of the startup check: it creates the directory private, and
// leaves no probe file behind to confuse a backup.
func TestEnsureStateDirCreatesAPrivateDirectory(t *testing.T) {
	cfg := Defaults()
	cfg.StateDir = filepath.Join(t.TempDir(), "a", "b")
	if err := cfg.EnsureStateDir(); err != nil {
		t.Fatalf("EnsureStateDir: %v", err)
	}
	fi, err := os.Stat(cfg.StateDir)
	if err != nil || !fi.IsDir() {
		t.Fatalf("state dir not created: %v", err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("state dir mode %v is readable by others", perm)
	}
	entries, _ := os.ReadDir(cfg.StateDir)
	if len(entries) != 0 {
		t.Errorf("left behind %v", entries)
	}
}

// A state-dir that is a file cannot hold the database; that has to fail at
// startup, not at the first write.
func TestEnsureStateDirRejectsAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Defaults()
	cfg.StateDir = file
	if err := cfg.EnsureStateDir(); err == nil {
		t.Fatal("a regular file was accepted as the state dir")
	}
}

// With no state-dir anywhere, the default follows XDG outside a container and
// is a fixed path inside one, so a mounted volume is where the data lands.
func TestDefaultStateDir(t *testing.T) {
	cleanEnv(t)
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(xdg, "claudication")
	if _, err := os.Stat("/.dockerenv"); err == nil {
		want = "/var/lib/claudication"
	}
	if cfg.StateDir != want {
		t.Errorf("StateDir = %q, want %q", cfg.StateDir, want)
	}
	for _, s := range cfg.Settings() {
		if s.Key == "state-dir" && s.Origin != FromDefault {
			t.Errorf("a defaulted state-dir reports origin %q", s.Origin)
		}
	}

	if want == "/var/lib/claudication" {
		return
	}
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", home)
	cfg, err = Load("")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".local", "state", "claudication"); cfg.StateDir != want {
		t.Errorf("StateDir = %q, want %q", cfg.StateDir, want)
	}

	// No home and no XDG: there is nowhere sensible to put credentials, and
	// guessing the working directory would scatter them.
	t.Setenv("HOME", "")
	if _, err := Load(""); err == nil {
		t.Error("Load with no HOME and no state-dir succeeded")
	}
}

// "~" is how operators write paths in YAML, and the shell is not there to
// expand it; an unexpanded "~" would create a directory literally named so.
func TestStateDirExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for in, want := range map[string]string{
		"~":            home,
		"~/state":      filepath.Join(home, "state"),
		"~other/state": "~other/state",
	} {
		if got := expandHome(in); got != want {
			t.Errorf("expandHome(%q) = %q, want %q", in, got, want)
		}
	}

	cleanEnv(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("state-dir: \"~/claud\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "claud"); cfg.StateDir != want {
		t.Errorf("state-dir = %q, want %q", cfg.StateDir, want)
	}
	for _, s := range cfg.Settings() {
		if s.Key == "state-dir" && s.Origin != FromFile {
			t.Errorf("a state-dir from the file reports origin %q", s.Origin)
		}
	}
}
