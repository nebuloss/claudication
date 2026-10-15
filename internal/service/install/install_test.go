package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A token works for as long as it lasts, for any number of runs, and not at
// all after.
func TestLinksExpire(t *testing.T) {
	l := New(time.Minute)
	now := time.Now()
	l.now = func() time.Time { return now }
	b := Bundle{Key: "laptop", Files: []File{{ClientID: "codex", Client: "Codex", Path: ".codex/config.toml", Body: "x"}}}

	token, _, err := l.Create(b)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, ok := l.Get(token); !ok {
			t.Fatalf("run %d: a live token did not work", i+1)
		}
	}
	now = now.Add(2 * time.Minute)
	if _, ok := l.Get(token); ok {
		t.Error("an expired token worked")
	}
	if _, ok := l.Get("nonesuch"); ok {
		t.Error("an unknown token worked")
	}
}

// client= picks clients; a name the bundle lacks picks nothing.
func TestBundleFor(t *testing.T) {
	b := Bundle{Files: []File{
		{ClientID: "codex", Path: ".codex/config.toml"},
		{ClientID: "codex", Path: ".codex/claude-models.json"},
		{ClientID: "crush", Path: ".config/crush/crush.json"},
	}}
	if n := len(b.For("").Files); n != 3 {
		t.Errorf("no client named: %d files", n)
	}
	if n := len(b.For("codex").Files); n != 2 {
		t.Errorf("codex: %d files", n)
	}
	if n := len(b.For("crush, codex").Files); n != 3 {
		t.Errorf("crush, codex: %d files", n)
	}
	if n := len(b.For("nope").Files); n != 0 {
		t.Errorf("an unknown client installed %d files", n)
	}
}

// Nothing a bundle names may land outside the home directory.
func TestBundleStaysUnderHome(t *testing.T) {
	for _, p := range []string{"/etc/passwd", "../x", ".codex/../../x", "a/./b", "a b", "a;b", "$HOME/x", ""} {
		b := Bundle{Files: []File{{Path: p, Body: "x"}}}
		if err := b.Validate(); err == nil {
			t.Errorf("%q was accepted", p)
		}
		b = Bundle{Files: []File{{Path: ".ok", WindowsPath: p, Body: "x"}}}
		if p != "" && b.Validate() == nil {
			t.Errorf("windows path %q was accepted", p)
		}
	}
	if err := (Bundle{Files: []File{{Path: ".config/crush/crush.json", WindowsPath: "AppData/Local/crush/crush.json", Body: "x"}}}).Validate(); err != nil {
		t.Errorf("a real bundle was refused: %v", err)
	}
}

// The shell script, run for real against a scratch home: it writes every file
// byte for byte, sources the env file from the profile once, and on a second
// run keeps what it replaces.
func TestShellScriptWritesTheFiles(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Quotes, dollars, backticks and a line that looks like a terminator:
	// none of it may be read by the shell.
	tricky := "a = '$HOME' \"`x`\"\nCLAUDICATION_EOF\n\\n end"
	env := "export ANTHROPIC_AUTH_TOKEN=clc_x\nclaude() {\n  command claude \"$@\"\n}\n"
	b := Bundle{Key: "laptop", Files: []File{
		{Client: "Codex", Path: ".codex/config.toml", Body: tricky},
		{Client: "Claude Code", Path: ".config/claudication/claude-code.sh", Env: true, Body: env},
	}}

	run := func() {
		t.Helper()
		cmd := exec.Command(sh)
		cmd.Stdin = strings.NewReader(Shell(b))
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("script failed: %v\n%s", err, out)
		}
	}
	run()
	got, err := os.ReadFile(filepath.Join(home, ".codex/config.toml"))
	if err != nil || string(got) != tricky+"\n" {
		t.Errorf("config.toml = %q (%v)", got, err)
	}
	if got, _ := os.ReadFile(filepath.Join(home, ".config/claudication/claude-code.sh")); string(got) != env {
		t.Errorf("claude-code.sh = %q", got)
	}

	run()
	rc, _ := os.ReadFile(filepath.Join(home, ".bashrc"))
	if n := strings.Count(string(rc), "claude-code.sh\" ] && ."); n != 1 {
		t.Errorf("the profile sources the file %d times, want once:\n%s", n, rc)
	}
	if !strings.HasPrefix(string(rc), "# mine\n") {
		t.Error("the profile's own contents were not kept")
	}
	backups, _ := filepath.Glob(filepath.Join(home, ".codex/config.toml.bak.*"))
	if len(backups) != 1 {
		t.Errorf("backups after the second run: %v", backups)
	}
}

// The PowerShell script carries files base64-encoded and sets an env file's
// variables for the user, quoted so nothing in them is read as PowerShell.
func TestPowerShellScript(t *testing.T) {
	b := Bundle{Key: "o'brien", Files: []File{
		{Client: "crush", Path: ".config/crush/crush.json", WindowsPath: "AppData/Local/crush/crush.json", Body: `{"$x": "y"}`},
		{Client: "Claude Code", Path: ".config/claudication/claude-code.sh", Env: true,
			Body: "export ANTHROPIC_BASE_URL=https://gw\nexport ANTHROPIC_MODEL='claude-large'\nclaude() {\n}\n"},
	}}
	ps := PowerShell(b)
	for _, want := range []string{
		`Put-ClaudicationFile 'AppData\Local\crush\crush.json' 'eyIkeCI6ICJ5In0='`,
		`[Environment]::SetEnvironmentVariable('ANTHROPIC_BASE_URL', 'https://gw', 'User')`,
		`[Environment]::SetEnvironmentVariable('ANTHROPIC_MODEL', 'claude-large', 'User')`,
		`'o''brien'`,
	} {
		if !strings.Contains(ps, want) {
			t.Errorf("missing %s in:\n%s", want, ps)
		}
	}
	if strings.Contains(ps, "claude()") {
		t.Error("a shell function leaked into the PowerShell script")
	}
}
