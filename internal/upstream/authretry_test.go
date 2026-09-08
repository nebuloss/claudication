package upstream

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// A 401 usually means the access token aged out, not that the account is
// broken. Refreshing it and then excluding it from the retry — which is what
// adding it to `tried` does, since Acquire skips excluded accounts — spends
// the refresh and then passes over the account it just repaired. With one
// account connected, that turns a routine token expiry into a failed request.
func TestA401RefreshesAndRetriesTheSameAccount(t *testing.T) {
	var mu sync.Mutex
	var attempts int
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"message","usage":{"input_tokens":3,"output_tokens":4}}`))
	}))
	defer upstreamSrv.Close()

	p := &recordingPool{}
	r := &Relay{
		Pool:    p,
		Client:  upstreamSrv.Client(),
		Log:     slog.New(slog.DiscardHandler),
		BaseURL: upstreamSrv.URL,
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	res := r.Do(rec, req, "anthropic", "/v1/messages", []byte("{}"), Prologue{})

	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200: the refreshed account must serve the retry, body = %s",
			res.Status, rec.Body.String())
	}
	if p.refreshes != 1 {
		t.Errorf("refreshes = %d, want exactly 1", p.refreshes)
	}
	if p.failures != 0 {
		t.Errorf("failures = %d; an expired access token is not the account failing", p.failures)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 2 {
		t.Errorf("upstream attempts = %d, want 2", attempts)
	}
}

// One refresh per account per request. A second is a loop, not a repair: if
// the account still answers 401 after its token was replaced, the account
// really is broken and should cool down.
func TestARepeated401StopsAfterOneRefresh(t *testing.T) {
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error"}}`))
	}))
	defer upstreamSrv.Close()

	p := &recordingPool{}
	r := &Relay{
		Pool:    p,
		Client:  upstreamSrv.Client(),
		Log:     slog.New(slog.DiscardHandler),
		BaseURL: upstreamSrv.URL,
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	res := r.Do(rec, req, "anthropic", "/v1/messages", []byte("{}"), Prologue{})

	if p.refreshes != 1 {
		t.Errorf("refreshes = %d, want 1", p.refreshes)
	}
	if p.failures == 0 {
		t.Error("an account still refusing after a refresh should cool down")
	}
	// And the client still gets the upstream's own words.
	if res.Status != http.StatusUnauthorized {
		t.Errorf("status = %d, want the upstream's 401 relayed", res.Status)
	}
	if !strings.Contains(rec.Body.String(), "authentication_error") {
		t.Errorf("body = %q, want the upstream's error verbatim", rec.Body.String())
	}
}
