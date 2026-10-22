package install

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// What the admin UI sends for an update, cut down to what the rules turn on:
// the Claude Code env file with the example key's line where the key goes,
// and crush's provider without its api_key, owning only the model paths.
const (
	ccUpdate = `# Claude Code, pointed at a claudication gateway.
export ANTHROPIC_BASE_URL=https://gw
export ANTHROPIC_AUTH_TOKEN=clc_...
export ANTHROPIC_MODEL=large
export ANTHROPIC_SMALL_FAST_MODEL=quick
claude() {
  command claude --settings '{"modelPicker":{"options":[{"model":"deep","behavesAs":"claude-opus-5-5"}]}}' "$@"
}
`
	ccInstalled = `# Claude Code, pointed at a claudication gateway.
export ANTHROPIC_BASE_URL=https://gw
export ANTHROPIC_AUTH_TOKEN=clc_0123456789abcdefREAL
export ANTHROPIC_MODEL=large
export ANTHROPIC_SMALL_FAST_MODEL=small
claude() {
  command claude --settings '{"modelPicker":{"options":[]}}' "$@"
}
`
	crushUpdate = `{
  "providers": {"claudication": {"name": "Claudication Gateway", "base_url": "https://gw", "models": [{"id": "large"}, {"id": "deep"}]}},
  "models": {"large": {"model": "large", "provider": "claudication"}, "small": {"model": "small", "provider": "claudication"}}
}
`
	crushInstalled = `{
  "providers": {
    "openai": {"api_key": "sk-mine"},
    "claudication": {"name": "Claudication Gateway", "base_url": "https://gw", "api_key": "clc_0123456789abcdefREAL", "models": [{"id": "large"}]}
  },
  "models": {"large": {"model": "large", "provider": "claudication"}}
}
`
	realKey = "clc_0123456789abcdefREAL"
)

func updateBundle() Bundle {
	return Bundle{Key: "laptop", Update: true, Files: []File{
		{Client: "Claude Code", Path: ".config/claudication/claude-code.sh", Env: true, Body: ccUpdate,
			Merge: &Merge{Format: "env", Keep: []string{"ANTHROPIC_AUTH_TOKEN"}}},
		{Client: "crush", Path: ".config/crush/crush.json", Body: crushUpdate, Existing: true,
			Merge: &Merge{Format: "json", Owns: []string{"providers.claudication.models", "models.large", "models.small"}}},
	}}
}

// runShellStatus is runShell for a script that may fail: its output and exit
// status.
func runShellStatus(t *testing.T, b Bundle, home, path string) (string, int) {
	t.Helper()
	cmd := exec.Command(filepath.Join(path, "sh"))
	cmd.Stdin = strings.NewReader(Shell(b))
	cmd.Env = []string{"HOME=" + home, "PATH=" + path}
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return string(out), exit.ExitCode()
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(out), 0
}

// An update, run for real against a machine set up with the key: the env
// file is rewritten from the update but for the key's line, which is the one
// on disk; crush's model list and picks are the update's and its provider's
// api_key, name and the operator's other provider stay. A second run changes
// nothing, and neither the script nor any file it writes holds a key it was
// not already holding.
func TestUpdateKeepsTheKeyOnDisk(t *testing.T) {
	for _, engine := range []string{"python3", "node"} {
		t.Run(engine, func(t *testing.T) {
			path := toolbox(t, engine)
			home := t.TempDir()
			cc := filepath.Join(home, ".config/claudication/claude-code.sh")
			crush := filepath.Join(home, ".config/crush/crush.json")
			write(t, cc, ccInstalled)
			write(t, crush, crushInstalled)

			b := updateBundle()
			if err := b.Validate(); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(Shell(b), realKey) || strings.Contains(PowerShell(b), realKey) {
				t.Fatal("the scripts carry a key")
			}
			if out, code := runShellStatus(t, b, home, path); code != 0 {
				t.Fatalf("update failed (%d):\n%s", code, out)
			}

			want := strings.Replace(ccUpdate, "export ANTHROPIC_AUTH_TOKEN=clc_...", "export ANTHROPIC_AUTH_TOKEN="+realKey, 1)
			if got := read(t, cc); got != want {
				t.Errorf("claude-code.sh after the update:\n%s\nwant:\n%s", got, want)
			}

			var got map[string]any
			if err := json.Unmarshal([]byte(read(t, crush)), &got); err != nil {
				t.Fatal(err)
			}
			providers := got["providers"].(map[string]any)
			ours := providers["claudication"].(map[string]any)
			if ours["api_key"] != realKey || ours["name"] != "Claudication Gateway" {
				t.Errorf("the provider's key or name did not stay: %v", ours)
			}
			if ids := mustJSON(ours["models"]); ids != `[{"id":"large"},{"id":"deep"}]` {
				t.Errorf("the model list is %s, want the update's", ids)
			}
			if providers["openai"] == nil || got["models"].(map[string]any)["small"] == nil {
				t.Errorf("the operator's provider or the small pick is missing: %v", got)
			}

			first := read(t, crush)
			if out, code := runShellStatus(t, b, home, path); code != 0 {
				t.Fatalf("second update failed:\n%s", out)
			}
			if read(t, crush) != first || read(t, cc) != want {
				t.Error("a second update changed something")
			}
			if backups, _ := filepath.Glob(cc + ".bak.*"); len(backups) == 0 {
				t.Error("no backup of claude-code.sh")
			}
		})
	}
}

// An update finds nothing to update on a machine never set up, or set up
// without the key's line, and writes nothing at all: not even the files it
// could have merged.
func TestUpdateRefusesAMachineNeverSetUp(t *testing.T) {
	path := toolbox(t, "python3")
	cc := ".config/claudication/claude-code.sh"
	crush := ".config/crush/crush.json"

	for name, setup := range map[string]map[string]string{
		"no env file":       {crush: crushInstalled},
		"no key in it":      {cc: strings.Replace(ccInstalled, "export ANTHROPIC_AUTH_TOKEN="+realKey+"\n", "", 1), crush: crushInstalled},
		"no crush.json":     {cc: ccInstalled},
		"an empty env file": {cc: "", crush: crushInstalled},
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			for rel, body := range setup {
				write(t, filepath.Join(home, rel), body)
			}
			out, code := runShellStatus(t, updateBundle(), home, path)
			if code == 0 || !strings.Contains(out, NeverSetUp) {
				t.Errorf("exit %d, want a refusal saying %q:\n%s", code, NeverSetUp, out)
			}
			for rel, body := range setup {
				if read(t, filepath.Join(home, rel)) != body {
					t.Errorf("%s was changed by a refused update", rel)
				}
			}
			for _, rel := range []string{cc, crush} {
				if _, ok := setup[rel]; ok {
					continue
				}
				if _, err := os.Stat(filepath.Join(home, rel)); err == nil {
					t.Errorf("%s was written by a refused update", rel)
				}
			}
			if backups, _ := filepath.Glob(filepath.Join(home, ".config/*/*.bak.*")); len(backups) != 0 {
				t.Errorf("a refused update made backups: %v", backups)
			}
		})
	}
}

// On Windows the env file is the user's variables: the update sets every one
// but the key, checks the key is set before anything else, and checks the
// files it merges into are there. There is no pwsh here, so this reads the
// script.
func TestPowerShellUpdateKeepsTheKey(t *testing.T) {
	ps := PowerShell(updateBundle())
	for _, want := range []string{
		"# An update: the model names and what they serve. The key stays as it is here.",
		`if (-not [Environment]::GetEnvironmentVariable('ANTHROPIC_AUTH_TOKEN', 'User')) { throw 'claudication: the user variable ANTHROPIC_AUTH_TOKEN is not set: ` + NeverSetUp + `.' }`,
		`if (-not (Test-Path (Join-Path $HOME '.config\crush\crush.json'))) { throw 'claudication: there is no ~\.config\crush\crush.json: ` + NeverSetUp + `.' }`,
		`[Environment]::SetEnvironmentVariable('ANTHROPIC_SMALL_FAST_MODEL', 'quick', 'User')`,
		`Merge-ClaudicationFile '.config\crush\crush.json' 'json' '`,
		`@('providers.claudication.models', 'models.large', 'models.small')`,
	} {
		if !strings.Contains(ps, want) {
			t.Errorf("missing %s in:\n%s", want, ps)
		}
	}
	if strings.Contains(ps, "SetEnvironmentVariable('ANTHROPIC_AUTH_TOKEN'") || strings.Contains(ps, "clc_") {
		t.Errorf("the update sets the key:\n%s", ps)
	}
	// The checks come before anything is written.
	check := strings.Index(ps, "GetEnvironmentVariable('ANTHROPIC_AUTH_TOKEN'")
	if first := strings.Index(ps, "SetEnvironmentVariable("); check < 0 || first < check {
		t.Error("a variable is set before the key is checked")
	}
	if first := strings.Index(ps, "\nMerge-ClaudicationFile "); first < check {
		t.Error("a file is merged before the key is checked")
	}
}

// What an update may and may not be: an env file names the variables it
// keeps, an env merge is only an env file's, and an update never writes an
// env file whole, which would put the example key where the key was.
func TestUpdateIsValidated(t *testing.T) {
	ok := updateBundle()
	for name, edit := range map[string]func(*Bundle){
		"an env file written whole":     func(b *Bundle) { b.Files[0].Merge = nil },
		"an env merge keeping nothing":  func(b *Bundle) { b.Files[0].Merge.Keep = nil },
		"a variable that is not a name": func(b *Bundle) { b.Files[0].Merge.Keep = []string{"A;rm -rf"} },
		"an env merge owning paths":     func(b *Bundle) { b.Files[0].Merge.Owns = []string{"x"} },
		"an env merge of a JSON file":   func(b *Bundle) { b.Files[1].Merge = &Merge{Format: "env", Keep: []string{"X"}} },
		"a JSON merge keeping a name":   func(b *Bundle) { b.Files[1].Merge.Keep = []string{"X"} },
		"an env file merged as JSON":    func(b *Bundle) { b.Files[0].Merge = &Merge{Format: "json", Owns: []string{"x"}} },
	} {
		b := updateBundle()
		edit(&b)
		if b.Validate() == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := ok.Validate(); err != nil {
		t.Errorf("a real update was refused: %v", err)
	}
	// An install may still write its env file whole: it carries the key.
	install := updateBundle()
	install.Update = false
	install.Files[0].Merge = nil
	if err := install.Validate(); err != nil {
		t.Errorf("an install's env file was refused: %v", err)
	}
}

// A client's update link lives beside its install link: making one leaves
// the install's record of where it ran, fetching it says it was an update,
// and a new key forgets both.
func TestUpdateLinksAreTheirOwn(t *testing.T) {
	l := New(time.Minute)
	files := []File{{ClientID: "codex", Client: "Codex", Path: ".codex/config.toml", Body: "x"}}
	token, _, err := l.Create(Bundle{ClientID: "c1", Key: "laptop", Files: files})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Take(token, "10.0.0.7", "sh"); err != nil {
		t.Fatal(err)
	}
	if st := l.UpdateStateOf("c1"); st.State != None {
		t.Errorf("an update state before any update link: %+v", st)
	}
	up, _, err := l.Create(Bundle{ClientID: "c1", Key: "laptop", Files: files, Update: true})
	if err != nil {
		t.Fatal(err)
	}
	if st := l.StateOf("c1"); st.State != Used || st.Used.IP != "10.0.0.7" {
		t.Errorf("the install's record after an update link was made: %+v", st)
	}
	if st := l.UpdateStateOf("c1"); st.State != Waiting || st.Token != up {
		t.Errorf("the update link: %+v", st)
	}
	b, err := l.Take(up, "10.0.0.9", "ps1")
	if err != nil || !b.Update {
		t.Fatalf("the update's fetch: %+v, %v", b, err)
	}
	if st := l.UpdateStateOf("c1"); st.State != Used || st.Used.IP != "10.0.0.9" {
		t.Errorf("the update after its fetch: %+v", st)
	}
	if st := l.StateOf("c1"); st.Used.IP != "10.0.0.7" {
		t.Errorf("the update's fetch overwrote the install's: %+v", st)
	}
	up, _, _ = l.Create(Bundle{ClientID: "c1", Key: "laptop", Files: files, Update: true})
	l.Forget("c1")
	if _, err := l.Take(up, "", "sh"); !errors.Is(err, ErrUnknown) {
		t.Errorf("an update link outlived a new key: %v", err)
	}
	if l.StateOf("c1").State != None || l.UpdateStateOf("c1").State != None {
		t.Error("a forgotten client still has a link")
	}
}

// An agent's update links replace nothing: the operator's command on the
// client's page stays waiting, two agents' both work, each once, and each is
// gone at its time; a new key forgets them with the rest.
func TestAgentUpdateLinksReplaceNothing(t *testing.T) {
	l := New(time.Minute)
	now := time.Now()
	l.now = func() time.Time { return now }
	files := []File{{ClientID: "codex", Client: "Codex", Path: ".codex/config.toml", Body: "x"}}
	page, _, err := l.Create(Bundle{ClientID: "c1", Key: "laptop", Files: files, Update: true, Base: "https://gw.example"})
	if err != nil {
		t.Fatal(err)
	}
	a1, _, err := l.Create(Bundle{ClientID: "c1", Key: "laptop", Files: files, Update: true, ByAgent: true})
	if err != nil {
		t.Fatal(err)
	}
	a2, _, err := l.Create(Bundle{ClientID: "c1", Key: "laptop", Files: files, Update: true, ByAgent: true})
	if err != nil {
		t.Fatal(err)
	}
	st := l.UpdateStateOf("c1")
	if st.State != Waiting || st.Token != page || st.Commands["sh"] != Command("https://gw.example", page, false) {
		t.Errorf("the page's command after an agent's: %+v", st)
	}
	for _, tok := range []string{a2, a1} {
		if b, err := l.Take(tok, "", "sh"); err != nil || !b.ByAgent {
			t.Errorf("an agent's link: %+v %v", b, err)
		}
		if _, err := l.Take(tok, "", "sh"); !errors.Is(err, ErrUsed) {
			t.Errorf("an agent's link twice: %v", err)
		}
	}
	if _, err := l.Take(page, "", "sh"); err != nil {
		t.Errorf("the page's command after the agents' were run: %v", err)
	}
	if _, _, err := l.Create(Bundle{ClientID: "c1", Files: files, ByAgent: true}); err == nil {
		t.Error("an agent's install link")
	}
	for i := 0; i < maxAgentLinks; i++ {
		if _, _, err := l.Create(Bundle{ClientID: "c1", Files: files, Update: true, ByAgent: true}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := l.Create(Bundle{ClientID: "c1", Files: files, Update: true, ByAgent: true}); err == nil {
		t.Error("no bound on an agent's waiting links")
	}
	late, _, _ := l.Create(Bundle{ClientID: "c2", Files: files, Update: true, ByAgent: true})
	now = now.Add(2 * time.Minute)
	if _, err := l.Take(late, "", "sh"); !errors.Is(err, ErrUnknown) {
		t.Errorf("an agent's link past its time: %v", err)
	}
	if _, _, err := l.Create(Bundle{ClientID: "c1", Files: files, Update: true, ByAgent: true}); err != nil {
		t.Fatal(err)
	}
	l.Forget("c1")
	if len(l.byAgent) != 0 {
		t.Errorf("a new key left an agent's links: %d", len(l.byAgent))
	}
}
