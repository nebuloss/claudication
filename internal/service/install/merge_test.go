package install

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// What the admin UI sends for crush and Codex, cut down to what the merge
// rules turn on.
const (
	crushNew = `{
  "$schema": "https://charm.land/crush.json",
  "providers": {"claudication": {"base_url": "https://gw", "api_key": "clc_new", "models": [{"id": "claude-large"}]}},
  "models": {"large": {"model": "claude-large", "provider": "claudication"}, "small": {"model": "claude-small", "provider": "claudication"}}
}
`
	crushMine = `{
  // my providers
  "providers": {
    "openai": {"api_key": "sk-mine"},
    "claudication": {"api_key": "clc_old", "stale": true},
  },
  "models": {"large": {"model": "gpt-5", "provider": "openai"}},
  "options": {"debug": true}
}
`
	codexNew = `# Codex CLI, pointed at a claudication gateway.
model_provider = "claudication"
model = "claude-large"

[model_providers.claudication]
name = "claudication"
base_url = "https://gw/v1"
`
	codexMine = `# mine
model = "gpt-5"
approval_policy = "never"

[mcp_servers.git]
command = "uvx"

[model_providers.claudication]
base_url = "https://old/v1"

[model_providers.claudication.extra]
x = 1
`
)

func mergeBundle() Bundle {
	return Bundle{Key: "laptop", Files: []File{
		{Client: "crush", Path: ".config/crush/crush.json", Body: crushNew,
			Merge: &Merge{Format: "json", Owns: []string{"providers.claudication", "models.large", "models.small"}}},
		{Client: "Codex", Path: ".codex/config.toml", Body: codexNew, Merge: &Merge{Format: "toml"}},
	}}
}

// toolbox is a PATH holding the tools the script needs, plus the JSON
// engines named, and no others: which engine merges is then the test's
// choice rather than the machine's.
func toolbox(t *testing.T, engines ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range append([]string{"sh", "cat", "cp", "mv", "mkdir", "dirname", "date", "rm", "mktemp", "awk"}, engines...) {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("no %s here", tool)
		}
		if err := os.Symlink(p, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runShell(t *testing.T, b Bundle, home, path string) string {
	t.Helper()
	cmd := exec.Command(filepath.Join(path, "sh"))
	cmd.Stdin = strings.NewReader(Shell(b))
	cmd.Env = []string{"HOME=" + home, "PATH=" + path}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	return string(out)
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A client's own config keeps everything that is not the gateway's, the
// gateway's sections come out exactly as sent, and a second run changes
// nothing — with python3 and with node, which must agree.
func TestShellScriptMergesClientConfigs(t *testing.T) {
	var merged []string
	for _, engine := range []string{"python3", "node"} {
		t.Run(engine, func(t *testing.T) {
			path := toolbox(t, engine)
			home := t.TempDir()
			crush := filepath.Join(home, ".config/crush/crush.json")
			codex := filepath.Join(home, ".codex/config.toml")
			write(t, crush, crushMine)
			write(t, codex, codexMine)

			runShell(t, mergeBundle(), home, path)
			first := read(t, crush)
			var got, want map[string]any
			if err := json.Unmarshal([]byte(first), &got); err != nil {
				t.Fatalf("merged crush.json does not parse: %v\n%s", err, first)
			}
			_ = json.Unmarshal([]byte(crushNew), &want)
			providers := got["providers"].(map[string]any)
			if _, ok := providers["openai"]; !ok {
				t.Error("the operator's own provider was dropped")
			}
			if c, _ := json.Marshal(providers["claudication"]); string(c) != mustJSON(want["providers"].(map[string]any)["claudication"]) {
				t.Errorf("the gateway's provider is %s, want it exactly as sent", c)
			}
			if got["models"].(map[string]any)["large"].(map[string]any)["model"] != "claude-large" {
				t.Error("the large pick was not taken")
			}
			if got["options"] == nil || got["$schema"] == nil {
				t.Errorf("options kept and $schema added, got %v", got)
			}

			toml := read(t, codex)
			for _, keep := range []string{"approval_policy = \"never\"", "[mcp_servers.git]", "command = \"uvx\"", "# mine"} {
				if !strings.Contains(toml, keep) {
					t.Errorf("config.toml lost %q:\n%s", keep, toml)
				}
			}
			for _, gone := range []string{`model = "gpt-5"`, "https://old/v1", "[model_providers.claudication.extra]"} {
				if strings.Contains(toml, gone) {
					t.Errorf("config.toml still has %q:\n%s", gone, toml)
				}
			}
			if strings.Index(toml, `model = "claude-large"`) > strings.Index(toml, "[") {
				t.Errorf("a top-level key landed under a table:\n%s", toml)
			}

			runShell(t, mergeBundle(), home, path)
			if again := read(t, crush); again != first {
				t.Errorf("a second run changed crush.json:\n%s\nthen\n%s", first, again)
			}
			if again := read(t, codex); again != toml {
				t.Errorf("a second run changed config.toml:\n%s\nthen\n%s", toml, again)
			}
			if backups, _ := filepath.Glob(crush + ".bak.*"); len(backups) == 0 {
				t.Error("no backup of crush.json")
			}
			merged = append(merged, first)
		})
	}
	if len(merged) == 2 && merged[0] != merged[1] {
		t.Errorf("python3 and node merge differently:\n%s\n%s", merged[0], merged[1])
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// A file that cannot be merged is left exactly as it was, with the gateway's
// version beside it: no JSON engine on the machine, or a file that does not
// parse. A file that is not there yet is simply written.
func TestShellScriptNeverReplacesWhatItCannotMerge(t *testing.T) {
	b := Bundle{Key: "laptop", Files: mergeBundle().Files[:1]}
	crushRel := ".config/crush/crush.json"

	home := t.TempDir()
	write(t, filepath.Join(home, crushRel), crushMine)
	out := runShell(t, b, home, toolbox(t))
	if read(t, filepath.Join(home, crushRel)) != crushMine {
		t.Error("crush.json was changed with nothing to merge it")
	}
	if read(t, filepath.Join(home, crushRel+".claudication")) != crushNew {
		t.Error("the gateway's version was not put beside it")
	}
	if !strings.Contains(out, "needs python3 or node") {
		t.Errorf("the script did not say why:\n%s", out)
	}

	home = t.TempDir()
	write(t, filepath.Join(home, crushRel), "[1, 2")
	runShell(t, b, home, toolbox(t, "python3"))
	if read(t, filepath.Join(home, crushRel)) != "[1, 2" {
		t.Error("a file that does not parse was replaced")
	}

	home = t.TempDir()
	runShell(t, b, home, toolbox(t))
	if read(t, filepath.Join(home, crushRel)) != crushNew {
		t.Error("a new file was not written as sent")
	}
}

func TestMergeIsValidated(t *testing.T) {
	file := func(m *Merge) Bundle { return Bundle{Files: []File{{Path: ".x", Body: "{}", Merge: m}}} }
	for _, bad := range []*Merge{
		{Format: "json"},
		{Format: "json", Owns: []string{"a b"}},
		{Format: "json", Owns: []string{"a;rm"}},
		{Format: "json", Owns: []string{"a..b"}},
		{Format: "toml", Owns: []string{"model"}},
		{Format: "yaml"},
	} {
		if file(bad).Validate() == nil {
			t.Errorf("%+v was accepted", *bad)
		}
	}
	if err := file(&Merge{Format: "json", Owns: []string{"$schema", "providers.claudication"}}).Validate(); err != nil {
		t.Errorf("a real merge was refused: %v", err)
	}
}

// The PowerShell script merges the same files through its own helpers.
func TestPowerShellScriptMerges(t *testing.T) {
	ps := PowerShell(mergeBundle())
	for _, want := range []string{
		"function Merge-ClaudicationJson",
		"function Merge-ClaudicationToml",
		`Merge-ClaudicationFile '.config\crush\crush.json' 'json' '`,
		`@('providers.claudication', 'models.large', 'models.small')`,
		`Merge-ClaudicationFile '.codex\config.toml' 'toml' '`,
	} {
		if !strings.Contains(ps, want) {
			t.Errorf("missing %s", want)
		}
	}
}

// A file with a stale place takes the gateway's keys back out of it on
// Windows, and only those; the stale place is validated like any path.
func TestPowerShellClearsAStalePlace(t *testing.T) {
	b := mergeBundle()
	b.Files[0].Stale = &Stale{WindowsPath: "AppData/Local/crush/crush.json", Owns: []string{"providers.claudication"}}
	if err := b.Validate(); err != nil {
		t.Fatalf("a real stale place was refused: %v", err)
	}
	ps := PowerShell(b)
	for _, want := range []string{
		"function Remove-ClaudicationJson",
		"function Clear-ClaudicationStale",
		`Clear-ClaudicationStale 'AppData\Local\crush\crush.json' @('providers.claudication')`,
	} {
		if !strings.Contains(ps, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(Shell(b), "AppData") {
		t.Error("the Unix script touched a Windows-only stale place")
	}
	for _, bad := range []*Stale{
		{WindowsPath: "../x", Owns: []string{"a"}},
		{WindowsPath: "AppData/x.json"},
		{WindowsPath: "AppData/x.json", Owns: []string{"a;b"}},
	} {
		b.Files[0].Stale = bad
		if b.Validate() == nil {
			t.Errorf("%+v was accepted", *bad)
		}
	}
}
