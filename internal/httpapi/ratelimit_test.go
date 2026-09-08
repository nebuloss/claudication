package httpapi

import (
	"context"
	"net/http"
	"testing"
)

// The anonymous budget is for requests that have not authenticated. Spending it
// on every request billed authenticated traffic against it too — and since
// requireAPIKey also fronts /v1/messages, that quietly capped real traffic at
// anon-per-minute (60) instead of the per-key limit (600). Claude Code's
// subagent fan-out crosses 60 requests a minute routinely, so this showed up as
// the gateway rate-limiting its own operator.
func TestAuthenticatedTrafficIsNotBilledToTheAnonymousBudget(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	_, plaintext, err := st.CreateKey(context.Background(), "busy", 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Comfortably past anon-per-minute, comfortably inside requests-per-minute.
	const requests = 120
	if srv.cfg.Limits.AnonPerMinute >= requests {
		t.Fatalf("fixture assumes anon-per-minute (%d) is below %d",
			srv.cfg.Limits.AnonPerMinute, requests)
	}

	throttled := 0
	for range requests {
		req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
		req.Header.Set("X-Api-Key", plaintext)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			throttled++
		}
	}
	if throttled > 0 {
		t.Errorf("%d of %d authenticated requests were rate limited; the anonymous budget is %d/min and must not apply to them",
			throttled, requests, srv.cfg.Limits.AnonPerMinute)
	}
}

// The other half of the same change: unauthenticated callers still hit a wall,
// and hit it before the database lookup.
func TestUnauthenticatedFloodIsStillThrottled(t *testing.T) {
	srv, _, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	budget := srv.cfg.Limits.AnonPerMinute
	throttled := 0
	for range budget * 2 {
		req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
		req.Header.Set("X-Api-Key", "clc_deadbeefdeadbeefdeadbeef")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			throttled++
		}
	}
	if throttled == 0 {
		t.Errorf("no request in %d bad-credential attempts was throttled; the anonymous budget is %d/min",
			budget*2, budget)
	}
}

// peek must not consume, or it would be indistinguishable from allow.
func TestPeekDoesNotSpendAToken(t *testing.T) {
	l := newLimiter()
	for range 100 {
		if !l.peek("ip", 3) {
			t.Fatal("peek refused on a bucket nothing has spent from")
		}
	}
	for i := range 3 {
		if !l.allow("ip", 3) {
			t.Fatalf("allow refused at %d, want the full budget still available", i)
		}
	}
	if l.allow("ip", 3) {
		t.Error("allow granted a fourth token from a budget of 3")
	}
	if l.peek("ip", 3) {
		t.Error("peek reported room in an exhausted bucket")
	}
}
