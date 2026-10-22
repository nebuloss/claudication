package install

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A link serves one fetch while it lasts: the first takes the bundle and
// records where it went, the second is told it was used, and an unused one
// past its time is expired. A new link replaces the client's last.
func TestLinksAreSingleUse(t *testing.T) {
	l := New(time.Minute)
	now := time.Now()
	l.now = func() time.Time { return now }
	b := Bundle{ClientID: "c1", Key: "laptop",
		Files: []File{{ClientID: "codex", Client: "Codex", Path: ".codex/config.toml", Body: "x"}}}

	if st := l.StateOf("c1"); st.State != None {
		t.Errorf("before any link: %+v", st)
	}
	token, _, err := l.Create(b)
	if err != nil {
		t.Fatal(err)
	}
	if st := l.StateOf("c1"); st.State != Waiting || st.Token != token {
		t.Errorf("made: %+v", st)
	}
	got, err := l.Take(token, "10.0.0.7", "sh")
	if err != nil || len(got.Files) != 1 || got.Files[0].Body != "x" {
		t.Fatalf("first fetch: %+v, %v", got, err)
	}
	if _, err := l.Take(token, "10.0.0.8", "sh"); !errors.Is(err, ErrUsed) {
		t.Errorf("second fetch: %v, want ErrUsed", err)
	}
	st := l.StateOf("c1")
	if st.State != Used || st.Used == nil || st.Used.IP != "10.0.0.7" || st.Used.Shell != "sh" || st.Token != "" {
		t.Errorf("used: %+v", st)
	}

	// A fresh link, never fetched, expires.
	token, _, _ = l.Create(b)
	now = now.Add(2 * time.Minute)
	if st := l.StateOf("c1"); st.State != Expired {
		t.Errorf("past its time: %+v", st)
	}
	if _, err := l.Take(token, "10.0.0.7", "ps1"); !errors.Is(err, ErrUnknown) {
		t.Errorf("expired fetch: %v", err)
	}
	// A third replaces it: the second's token is no client's link.
	old := token
	token, _, _ = l.Create(b)
	if _, err := l.Take(old, "", "sh"); !errors.Is(err, ErrUnknown) {
		t.Errorf("a replaced token worked: %v", err)
	}
	if _, err := l.Take("nonesuch", "", "sh"); !errors.Is(err, ErrUnknown) {
		t.Errorf("an unknown token: %v", err)
	}
	l.Forget("c1")
	if _, err := l.Take(token, "", "sh"); !errors.Is(err, ErrUnknown) {
		t.Errorf("a forgotten client's token worked: %v", err)
	}
	if _, _, err := l.Create(Bundle{Files: b.Files}); err == nil {
		t.Error("a link with no client was made")
	}
}

// An expired link that was never fetched lets go of its files, which carry the
// client's key, at the next call rather than at the next restart — and still
// says it expired.
func TestAnExpiredLinkDropsItsKey(t *testing.T) {
	l := New(time.Minute)
	now := time.Now()
	l.now = func() time.Time { return now }
	if _, _, err := l.Create(Bundle{ClientID: "c1", Key: "laptop",
		Files: []File{{ClientID: "crush", Client: "crush", Path: ".config/crush/crush.json", Body: "clc_secret"}}}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	// Any call sweeps: another client's link being made is one.
	if _, _, err := l.Create(Bundle{ClientID: "c2", Key: "box",
		Files: []File{{ClientID: "crush", Client: "crush", Path: ".config/crush/crush.json", Body: "x"}}}); err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	k := l.byClient[linkKey{"c1", false}]
	held := len(k.bundle.Files) > 0 || k.bundle.Key != "" || k.token != ""
	l.mu.Unlock()
	if held {
		t.Error("an expired link still holds its files, key name or token")
	}
	if st := l.StateOf("c1"); st.State != Expired {
		t.Errorf("after the sweep: %+v, want expired", st)
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
