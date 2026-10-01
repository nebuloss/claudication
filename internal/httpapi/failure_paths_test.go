package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/secret"
	"claudication/internal/store"
)

func mustSealer(t *testing.T, dir string) *secret.Sealer {
	t.Helper()
	s, err := secret.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The first-run screen asks this before anything else; it must flip the
// moment a password exists, or the setup form stays offered to whoever finds
// the address next.
func TestSetupStatus(t *testing.T) {
	srv, _, _ := newTestServer(t)
	base := adminOnly(t, srv)

	read := func() map[string]any {
		status, body := call(t, base, http.MethodGet, "/admin/setup", "", nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d: %s", status, body)
		}
		return decode[map[string]any](t, body)
	}
	if got := read(); got["needs_setup"] != true || got["min_password_len"] != float64(store.MinPasswordLength) {
		t.Errorf("fresh: %v", got)
	}
	claim(t, base)
	if got := read(); got["needs_setup"] != false {
		t.Errorf("after setup: %v", got)
	}
}

// Every password endpoint is reachable by anyone who can reach the admin
// address, so each refuses a malformed body, refuses a wrong password without
// saying how it was wrong, and stops answering a guesser.
func TestPasswordEndpointsRefuseAndThrottle(t *testing.T) {
	srv, _, _ := newTestServer(t)
	srv.cfg.Limits.AnonPerMinute = 3
	base := adminOnly(t, srv)

	if status, _ := call(t, base, http.MethodPost, "/admin/setup", `{`, nil); status != http.StatusBadRequest {
		t.Errorf("malformed setup: %d", status)
	}
	c := claim(t, base)

	for _, tc := range []struct {
		name, method, path, body string
		cookie                   *http.Cookie
		status                   int
	}{
		{"login, empty", http.MethodPost, "/admin/session", `{"password":"  "}`, nil, http.StatusBadRequest},
		{"login, malformed", http.MethodPost, "/admin/session", `[`, nil, http.StatusBadRequest},
		{"login, wrong", http.MethodPost, "/admin/session", `{"password":"wrong-password"}`, nil, http.StatusUnauthorized},
		{"change, malformed", http.MethodPost, "/admin/password", `{`, c, http.StatusBadRequest},
		{"change, wrong current", http.MethodPost, "/admin/password",
			`{"current_password":"wrong-password","new_password":"another-long-one"}`, c, http.StatusUnauthorized},
		{"change, too short", http.MethodPost, "/admin/password",
			`{"current_password":"` + testPassword + `","new_password":"x"}`, c, http.StatusBadRequest},
		{"delete, wrong", http.MethodPost, "/admin/account/delete", `{"password":"wrong-password"}`, c, http.StatusUnauthorized},
		{"delete, malformed", http.MethodPost, "/admin/account/delete", `{`, c, http.StatusBadRequest},
	} {
		if status, body := call(t, base, tc.method, tc.path, tc.body, tc.cookie); status != tc.status {
			t.Errorf("%s: %d %s, want %d", tc.name, status, body, tc.status)
		}
	}
	// The wrong delete left the account in place.
	if !signsIn(t, base, testPassword) {
		t.Fatal("a refused delete removed the account")
	}

	// Each endpoint keeps its own bucket, and each runs out.
	for _, tc := range []struct{ path, body string }{
		{"/admin/session", `{"password":"wrong-password"}`},
		{"/admin/setup", `{"password":"another-long-one"}`},
		{"/admin/password", `{"current_password":"wrong-password","new_password":"another-long-one"}`},
	} {
		limited := false
		for i := 0; i < 6 && !limited; i++ {
			status, body := call(t, base, http.MethodPost, tc.path, tc.body, c)
			limited = status == http.StatusTooManyRequests && errType(t, body) == "rate_limit"
		}
		if !limited {
			t.Errorf("%s was never throttled", tc.path)
		}
	}
}

// A handler that panics answers 500 in the API's own shape and the server
// stays up; one that panics after it started writing leaves the response it
// began rather than appending an error to it.
func TestPanicsAreContained(t *testing.T) {
	srv, _, _ := newTestServer(t)
	chain := func(h http.HandlerFunc) http.Handler {
		return httpx.WithRequestContext(httpx.WithAccessLog(httpx.WithRecovery(h, srv.log), srv.log), srv.trustedProxies)
	}

	w := httptest.NewRecorder()
	chain(func(http.ResponseWriter, *http.Request) { panic("boom") }).
		ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusInternalServerError || errType(t, w.Body.Bytes()) != "internal_error" {
		t.Errorf("early panic: %d %s", w.Code, w.Body)
	}

	w = httptest.NewRecorder()
	chain(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "partial")
		panic("late")
	}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusOK || w.Body.String() != "partial" {
		t.Errorf("late panic: %d %q, want the started response left alone", w.Code, w.Body)
	}
}

// relayFailureFixture stands a relay-only gateway in front of an upstream that
// is already closed, so nothing the relay sends can leave the machine.
func relayFailureFixture(t *testing.T) (*Server, *store.Store, string, string) {
	t.Helper()
	srv, st, _ := newTestServer(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	srv.relay.BaseURL = dead.URL
	// Token refreshes go through the gateway's own client; keep those local
	// too.
	(&fakeAnthropic{}).install(srv)
	_, key, err := st.CreateKey(context.Background(), "k", store.KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.routes(role{gateway: true}))
	t.Cleanup(ts.Close)
	return srv, st, ts.URL, key
}

const tinyRequest = `{"model":"claude-sonnet-5","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`

// When no bytes reached the client, the failure is answered in the caller's
// own envelope with a status it can act on: 503 and "add an account" when
// there is none, and 502 when the upstream could not be reached or the
// gateway could not even get a token to send.
func TestRelayFailuresAnswerInTheClientsEnvelope(t *testing.T) {
	t.Run("no account", func(t *testing.T) {
		_, _, base, key := relayFailureFixture(t)
		resp := post(t, base+"/v1/messages", key, tinyRequest)
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusServiceUnavailable || errType(t, body) != "no_accounts" {
			t.Errorf("status = %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("upstream unreachable", func(t *testing.T) {
		srv, st, base, key := relayFailureFixture(t)
		if err := seedAccount(t, st, srv); err != nil {
			t.Fatal(err)
		}
		resp := post(t, base+"/v1/messages", key, tinyRequest)
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		// The network failure sidelines the only account, so the retry finds
		// none left. What went wrong is still the network, not a rate limit:
		// the answer says so, in Anthropic's own error shape, rather than
		// sending the operator to look at quotas.
		if resp.StatusCode != http.StatusBadGateway || errType(t, body) != "api_error" ||
			!strings.Contains(string(body), `"type":"error"`) {
			t.Errorf("status = %d: %s", resp.StatusCode, body)
		}
		if strings.Contains(string(body), "rate limited") {
			t.Errorf("a network failure was reported as a rate limit: %s", body)
		}
	})

	t.Run("token refresh refused", func(t *testing.T) {
		srv, st, base, key := relayFailureFixture(t)
		addAccount(t, srv, st, "x@example.com", "old", "dead-refresh", time.Now().Add(-time.Minute))
		resp := post(t, base+"/v1/messages", key, tinyRequest)
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusBadGateway || errType(t, body) != "api_error" {
			t.Errorf("status = %d: %s", resp.StatusCode, body)
		}
	})
}

// A port clash on the admin or docs listener is a startup failure, so a
// supervisor sees it, rather than a gateway that comes up with no way in. The
// relay's own port is released again, so a retry can bind it.
func TestRunFailsOnAListenerClash(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()

	for _, which := range []string{"admin", "docs", "relay"} {
		t.Run(which, func(t *testing.T) {
			_, st, cfg := newTestServer(t)
			switch which {
			case "admin":
				cfg.AdminListen = taken.Addr().String()
			case "docs":
				cfg.DocsListen = taken.Addr().String()
			case "relay":
				cfg.Listen = taken.Addr().String()
			}
			srv, err := New(cfg, slog.New(slog.DiscardHandler), st, mustSealer(t, cfg.StateDir))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = srv.Run(ctx)
			if err == nil || !strings.Contains(err.Error(), taken.Addr().String()) {
				t.Errorf("Run = %v, want a listen error naming %s", err, taken.Addr())
			}
		})
	}
}

// A docs listener comes up beside the relay and goes down with it.
func TestDocsListenerRunsAndDrains(t *testing.T) {
	_, st, cfg := newTestServer(t)
	cfg.DocsListen = "127.0.0.1:0"
	srv, err := New(cfg, slog.New(slog.DiscardHandler), st, mustSealer(t, cfg.StateDir))
	if err != nil {
		t.Fatal(err)
	}
	_, cancel, done := startServer(t, srv)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("did not drain")
	}
}

// A trusted-proxy list that does not parse would silently trust nobody, or
// everybody; it has to stop the gateway from starting instead.
func TestNewRefusesBadTrustedProxies(t *testing.T) {
	_, st, cfg := newTestServer(t)
	cfg.TrustedProxies = []string{"not-a-network"}
	if _, err := New(cfg, slog.New(slog.DiscardHandler), st, mustSealer(t, cfg.StateDir)); err == nil {
		t.Error("New accepted an unparseable trusted proxy")
	}
}
