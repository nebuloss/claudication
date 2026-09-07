package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/nebuloss/claudication/internal/config"
	"github.com/nebuloss/claudication/internal/secret"
	"github.com/nebuloss/claudication/internal/store"
)

func newTestServer(t *testing.T) (*Server, *store.Store, config.Config) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := config.Defaults()
	cfg.StateDir = dir
	cfg.Listen = "127.0.0.1:0"
	cfg.Shutdown.Grace = config.Duration(2 * time.Second)

	sealer, err := secret.Load(dir)
	if err != nil {
		t.Fatalf("secret.Load: %v", err)
	}

	srv, err := New(cfg, slog.New(slog.DiscardHandler), st, sealer)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv, st, cfg
}

// startServer runs the server on an ephemeral port and returns its base URL.
func startServer(t *testing.T, srv *Server) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	// Wait for the listener to accept.
	var base string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if addr := srv.Addr(); addr != "" {
			base = "http://" + addr
			if resp, err := http.Get(base + "/health"); err == nil {
				resp.Body.Close()
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if base == "" {
		cancel()
		t.Fatal("server did not start")
	}
	return base, cancel, done
}

func TestHealthIsUnauthenticated(t *testing.T) {
	srv, _, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	resp, err := http.Get(base + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Errorf("status field = %q", body["status"])
	}
	// Health must not leak operational detail.
	for _, leak := range []string{"accounts", "account_count", "state_dir", "keys"} {
		if _, ok := body[leak]; ok {
			t.Errorf("health response leaks %q", leak)
		}
	}
}

// The contract documents a HEAD /api/hello warm-up probe from Claude Code.
func TestWarmupProbe(t *testing.T) {
	srv, _, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	req, _ := http.NewRequest(http.MethodHead, base+"/api/hello", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestModelsRequiresAuth(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	// No credential.
	resp, err := http.Get(base + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no key: status = %d, want 401", resp.StatusCode)
	}

	// Wrong credential.
	req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer clc_deadbeefdeadbeef")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad key: status = %d, want 401", resp.StatusCode)
	}

	_, plaintext, err := st.CreateKey(context.Background(), "test", 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Both header styles must work: Claude Code may send either or both.
	for name, set := range map[string]func(*http.Request){
		"bearer":    func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+plaintext) },
		"x-api-key": func(r *http.Request) { r.Header.Set("X-Api-Key", plaintext) },
	} {
		t.Run(name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
			set(req)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				b, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, body = %s", resp.StatusCode, b)
			}
			// The contract requires a data array; discovery reads it directly.
			var body struct {
				Object string `json:"object"`
				Data   []any  `json:"data"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Data == nil {
				t.Error("data must be present (an empty array, not null)")
			}
		})
	}
}

func TestDeletedKeyIsRejected(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	ctx := context.Background()
	key, plaintext, err := st.CreateKey(ctx, "test", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteKey(ctx, key.ID); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+plaintext)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a deleted key", resp.StatusCode)
	}
}

// The S0 gate: a cancelled context (SIGINT or SIGTERM) must drain and return
// cleanly rather than being killed.
func TestGracefulShutdown(t *testing.T) {
	srv, _, _ := newTestServer(t)
	_, cancel, done := startServer(t, srv)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not shut down within 10s")
	}
}

func TestRateLimitPerKey(t *testing.T) {
	srv, st, _ := newTestServer(t)
	// A tiny per-key budget; the anon bucket stays generous so we are
	// certain we measured the key limiter and not the IP one.
	srv.cfg.Limits.RequestsPerMinute = 3
	srv.cfg.Limits.AnonPerMinute = 10000

	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	_, plaintext, err := st.CreateKey(context.Background(), "test", 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	var limited bool
	for i := 0; i < 6; i++ {
		req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+plaintext)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("expected a 429 after exceeding the per-key budget")
	}
}

func TestLimiterRefills(t *testing.T) {
	l := newLimiter()
	base := time.Now()
	l.now = func() time.Time { return base }

	// 60/min = 1/s, burst 60. Drain it.
	for i := 0; i < 60; i++ {
		if !l.allow("k", 60) {
			t.Fatalf("request %d denied while the bucket should still be full", i)
		}
	}
	if l.allow("k", 60) {
		t.Fatal("expected denial once the bucket is empty")
	}

	l.now = func() time.Time { return base.Add(2 * time.Second) }
	if !l.allow("k", 60) {
		t.Error("expected a refill after 2s")
	}
}

func TestLimiterDisabledAtZero(t *testing.T) {
	l := newLimiter()
	for i := 0; i < 1000; i++ {
		if !l.allow("k", 0) {
			t.Fatal("a limit of 0 must disable limiting")
		}
	}
}

func TestLimiterSweepEvictsIdle(t *testing.T) {
	l := newLimiter()
	base := time.Now()
	l.now = func() time.Time { return base }
	l.allow("k", 60)

	l.now = func() time.Time { return base.Add(time.Hour) }
	l.sweep(10 * time.Minute)

	l.mu.Lock()
	n := len(l.buckets)
	l.mu.Unlock()
	if n != 0 {
		t.Errorf("len(buckets) = %d, want 0 after sweep", n)
	}
}
