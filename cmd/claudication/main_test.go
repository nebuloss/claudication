package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"claudication/internal/store"
	"claudication/internal/version"
)

// stateDir points every command at a fresh, empty state directory and an
// empty config file, and returns both.
//
// The explicit -config matters: without it the commands read
// /etc/claudication/config.yaml when it exists, and a test must not pick up
// whatever the machine running it happens to have installed.
func stateDir(t *testing.T) (dir, cfg string) {
	t.Helper()
	dir = t.TempDir()
	t.Setenv("CLAUDICATION_STATE_DIR", dir)
	cfg = filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfg, []byte("# empty: defaults and environment only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, cfg
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	got := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		got <- string(b)
	}()
	runErr := fn()
	os.Stdout = saved
	w.Close()
	out := <-got
	r.Close()
	return out, runErr
}

// withStdin feeds s to the command as its standard input, which is how a
// provisioning script sets the password: `claudication passwd < secret`.
func withStdin(t *testing.T, s string) {
	t.Helper()
	f := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(f, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(f)
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdin
	os.Stdin = in
	t.Cleanup(func() { os.Stdin = saved; in.Close() })
}

// openStore opens the database a command just wrote, so a test can check the
// effect rather than the message.
func openStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(dir, "claudication.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// A typo has to be an error with a non-zero exit, not a silent no-op: a
// provisioning script that misspells a command must fail where it runs.
func TestRunDispatch(t *testing.T) {
	if err := run(nil); err == nil {
		t.Error("no command: want an error")
	}
	if err := run([]string{"frobnicate"}); err == nil || !strings.Contains(err.Error(), "frobnicate") {
		t.Errorf("unknown command: err = %v, want it named", err)
	}
	for _, help := range []string{"help", "--help", "-h"} {
		if err := run([]string{help}); err != nil {
			t.Errorf("%s: %v", help, err)
		}
	}
	out, err := captureStdout(t, func() error { return run([]string{"version"}) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != version.String() {
		t.Errorf("version printed %q, want %q", out, version.String())
	}
}

// The link is printed for a human to paste into a browser, and a wildcard bind
// address is not something a browser can reach.
func TestLoginURLIsBrowsable(t *testing.T) {
	for _, tc := range []struct{ listen, want string }{
		{"127.0.0.1:8317", "http://127.0.0.1:8317/?token=T"},
		{"0.0.0.0:9000", "http://127.0.0.1:9000/?token=T"},
		{":9000", "http://127.0.0.1:9000/?token=T"},
		{"[::]:9000", "http://127.0.0.1:9000/?token=T"},
		{"[::1]:9000", "http://[::1]:9000/?token=T"},
		{"gw.example:80", "http://gw.example:80/?token=T"},
		{"not-an-address", "http://127.0.0.1:8317/?token=T"},
	} {
		if got := loginURL(tc.listen, "T"); got != tc.want {
			t.Errorf("loginURL(%q) = %q, want %q", tc.listen, got, tc.want)
		}
	}
}

// keys list prints these; a 0 or a Go duration string would read wrongly to
// an operator scanning the table.
func TestHumanFormatting(t *testing.T) {
	for d, want := range map[time.Duration]string{
		time.Minute: "min", time.Hour: "hour", 24 * time.Hour: "day", 90 * time.Second: "1m30s",
	} {
		if got := shortPeriod(d); got != want {
			t.Errorf("shortPeriod(%s) = %q, want %q", d, got, want)
		}
	}
	for n, want := range map[int64]string{
		12: "12 B", 2048: "2.0 KiB", 3 << 20: "3.0 MiB", 5 << 30: "5.0 GiB",
	} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

// An explicit -config wins, and one that does not exist is an error rather
// than a silent fall-back to defaults: the operator named a file and expects
// it to be read.
func TestConfigResolution(t *testing.T) {
	if got := resolveConfigPath("/some/where.yaml"); got != "/some/where.yaml" {
		t.Errorf("explicit path = %q", got)
	}
	stateDir(t)
	if _, _, _, err := openState(context.Background(), "/nonexistent/claudication.yaml"); err == nil {
		t.Error("a missing explicit config opened anyway")
	}
}

// The key lifecycle from the shell, checked against what the database holds:
// this is how a headless deployment mints its first client credential.
func TestKeysCommand(t *testing.T) {
	dir, cfg := stateDir(t)

	if err := cmdKeys(nil); err == nil {
		t.Error("keys with no subcommand: want an error")
	}
	if err := cmdKeys([]string{"rotate"}); err == nil {
		t.Error("unknown subcommand: want an error")
	}
	if err := cmdKeys([]string{"add", "-config", cfg}); err == nil {
		t.Error("add without -name: want an error")
	}
	if err := cmdKeys([]string{"delete", "-config", cfg}); err == nil {
		t.Error("delete without -id: want an error")
	}
	if err := cmdKeys([]string{"add", "-bogus"}); err == nil {
		t.Error("unknown flag: want an error")
	}

	out, err := captureStdout(t, func() error { return cmdKeys([]string{"list", "-config", cfg}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "No API keys") {
		t.Errorf("empty list said %q", out)
	}

	out, err = captureStdout(t, func() error {
		return cmdKeys([]string{"add", "-config", cfg, "-name", "ci",
			"-rpm", "5", "-rate-period", "1h", "-token-budget", "1000"})
	})
	if err != nil {
		t.Fatal(err)
	}
	// The plaintext is shown once and must authenticate.
	var plaintext string
	for _, f := range strings.Fields(out) {
		if strings.HasPrefix(f, "clc_") {
			plaintext = f
		}
	}
	if plaintext == "" {
		t.Fatalf("no key printed: %q", out)
	}
	st := openStore(t, dir)
	key, err := st.Authenticate(context.Background(), plaintext)
	if err != nil {
		t.Fatalf("the printed key does not authenticate: %v", err)
	}
	if key.Name != "ci" || key.RPMLimit != 5 || key.Period() != time.Hour || key.TokenBudget != 1000 {
		t.Errorf("stored key = %+v", key)
	}

	out, err = captureStdout(t, func() error { return cmdKeys([]string{"list", "-config", cfg}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{key.ID, "ci", "5/hour", "1000", "never"} {
		if !strings.Contains(out, want) {
			t.Errorf("list is missing %q:\n%s", want, out)
		}
	}
	// The secret itself is never listed again.
	if strings.Contains(out, plaintext) {
		t.Error("keys list printed the plaintext key")
	}

	if _, err := captureStdout(t, func() error {
		return cmdKeys([]string{"delete", "-config", cfg, "-id", key.ID})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Authenticate(context.Background(), plaintext); err == nil {
		t.Error("a deleted key still authenticates")
	}
	if err := cmdKeys([]string{"delete", "-config", cfg, "-id", key.ID}); err == nil {
		t.Error("deleting a key twice succeeded")
	}
}

// passwd is both first-run setup and the way back in after a lost password,
// and it must work from a pipe so provisioning can drive it.
func TestPasswdFromAPipe(t *testing.T) {
	dir, cfg := stateDir(t)

	withStdin(t, "first-password-here\n")
	out, err := captureStdout(t, func() error { return cmdPasswd([]string{"-config", cfg}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Admin password set") {
		t.Errorf("first run said %q", out)
	}
	st := openStore(t, dir)
	if ok, err := st.VerifyAdmin(context.Background(), "first-password-here"); err != nil || !ok {
		t.Fatalf("password not stored: ok=%v err=%v", ok, err)
	}

	// Recovery does not ask for the old one.
	withStdin(t, "second-password-here")
	out, err = captureStdout(t, func() error { return cmdPasswd([]string{"-config", cfg}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Password changed") {
		t.Errorf("second run said %q", out)
	}
	if ok, _ := st.VerifyAdmin(context.Background(), "second-password-here"); !ok {
		t.Error("the new password does not sign in")
	}
	if ok, _ := st.VerifyAdmin(context.Background(), "first-password-here"); ok {
		t.Error("the old password still signs in")
	}

	if err := cmdPasswd([]string{"-nope"}); err == nil {
		t.Error("unknown flag: want an error")
	}
}

// login-url is the way into a fresh container without typing a password; it
// must refuse when there is no account to sign into, and otherwise print a
// link the gateway will honour exactly once.
func TestLoginURLCommand(t *testing.T) {
	dir, cfg := stateDir(t)
	t.Setenv("CLAUDICATION_LISTEN", "0.0.0.0:8999")

	if err := cmdLoginURL([]string{"-config", cfg}); err == nil || !strings.Contains(err.Error(), "passwd") {
		t.Errorf("no admin: err = %v, want a pointer to passwd", err)
	}

	st := openStore(t, dir)
	if err := st.CreateAdmin(context.Background(), "an-admin-password"); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return cmdLoginURL([]string{"-config", cfg, "-ttl", "1m"}) })
	if err != nil {
		t.Fatal(err)
	}
	link := strings.TrimSpace(out)
	prefix := "http://127.0.0.1:8999/?token="
	if !strings.HasPrefix(link, prefix) {
		t.Fatalf("link = %q", link)
	}
	token := strings.TrimPrefix(link, prefix)
	if err := st.SpendLoginLink(context.Background(), token); err != nil {
		t.Fatalf("the printed link is not spendable: %v", err)
	}
	if err := st.SpendLoginLink(context.Background(), token); err == nil {
		t.Error("the link spent twice")
	}
	if err := cmdLoginURL([]string{"-ttl", "soon"}); err == nil {
		t.Error("bad -ttl: want an error")
	}
}

// vacuum rewrites the whole database; afterwards it must still hold what it
// held before.
func TestVacuumKeepsTheData(t *testing.T) {
	dir, cfg := stateDir(t)
	if _, err := captureStdout(t, func() error {
		return cmdKeys([]string{"add", "-config", cfg, "-name", "kept"})
	}); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return cmdVacuum([]string{"-config", cfg}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "before") || !strings.Contains(out, "after") {
		t.Errorf("vacuum output = %q", out)
	}
	keys, err := openStore(t, dir).ListKeys(context.Background())
	if err != nil || len(keys) != 1 || keys[0].Name != "kept" {
		t.Errorf("after vacuum: keys = %+v, err = %v", keys, err)
	}
	if err := cmdVacuum([]string{"-x"}); err == nil {
		t.Error("unknown flag: want an error")
	}
}

// serve has to come up, answer, and drain cleanly on SIGINT — the same signal
// path systemd's stop and Ctrl-C both take.
func TestServeRunsAndStopsOnSignal(t *testing.T) {
	_, cfg := stateDir(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	t.Setenv("CLAUDICATION_LISTEN", addr)
	t.Setenv("CLAUDICATION_LOG_LEVEL", "error")

	done := make(chan error, 1)
	go func() { done <- cmdServe([]string{"-config", cfg}) }()

	// Only once it answers is the signal handler certainly installed; a
	// SIGINT before that would end the test binary instead.
	up := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		resp, err := http.Get("http://" + addr + "/health")
		if err == nil {
			resp.Body.Close()
			up = resp.StatusCode == http.StatusOK
			break
		}
		select {
		case err := <-done:
			t.Fatalf("serve exited before answering: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	if !up {
		t.Fatal("serve never answered /health")
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve returned %v after SIGINT, want a clean drain", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop on SIGINT")
	}
}

// A configuration that cannot be served is a startup error, not a gateway
// that comes up wrong.
func TestServeRefusesABadStart(t *testing.T) {
	_, cfg := stateDir(t)
	if err := cmdServe([]string{"-config", "/nonexistent.yaml"}); err == nil {
		t.Error("missing config: want an error")
	}
	if err := cmdServe([]string{"-what"}); err == nil {
		t.Error("unknown flag: want an error")
	}
	t.Setenv("CLAUDICATION_TRUSTED_PROXIES", "not-a-cidr")
	if err := cmdServe([]string{"-config", cfg}); err == nil {
		t.Error("an unparseable trusted proxy started anyway")
	}
}
