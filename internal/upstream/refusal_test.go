package upstream

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"claudication/internal/pool"
	"claudication/internal/store"
)

// onePool is a Pool stand-in with a single account, which is the case that
// exposed this: with nothing to fail over to, a retryable refusal used to be
// swallowed and replaced with the gateway's own error.
type onePool struct {
	handedOut int
}

func (p *onePool) Acquire(_ context.Context, _ string, exclude map[string]bool) (pool.Lease, error) {
	if exclude["only"] {
		return pool.Lease{}, pool.ErrAllCoolingUp
	}
	p.handedOut++
	return pool.Lease{
		Account:     store.Account{ID: "only", Email: "only@example.com"},
		AccessToken: "token",
	}, nil
}

func (p *onePool) ReportFailure(string, pool.FailureKind, string) {}
func (p *onePool) ReportSuccess(string)                           {}
func (p *onePool) Refresh(context.Context, string) error          { return nil }

// The rule Lane A exists for: the upstream's error reaches the client
// unmodified. Claude Code decides whether to retry, and whether to disable a
// capability, by reading that body — and a per-model limit is only visible in
// it. Answering with our own "every connected account is rate limited" hides
// which model was refused and when it comes back.
func TestARefusalReachesTheClientVerbatim(t *testing.T) {
	const upstreamBody = `{"type":"error","error":{"type":"rate_limit_error",` +
		`"message":"This request would exceed your organization's weekly limit for claude-opus-5. ` +
		`The limit resets at 2026-09-08T12:00:00Z."}}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "3600")
		w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	relay := &Relay{Pool: &onePool{}, Client: upstream.Client(), BaseURL: upstream.URL}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	res := relay.Do(rec, req, "anthropic", "/v1/messages", []byte("{}"), Prologue{})

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want the upstream's 429 (a substituted error is the bug)", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if string(body) != upstreamBody {
		t.Errorf("body was not forwarded unmodified:\n got %s\nwant %s", body, upstreamBody)
	}
	// The reset is the only part the caller can act on.
	if got := rec.Header().Get("Retry-After"); got != "3600" {
		t.Errorf("Retry-After = %q, want the upstream's", got)
	}
	if got := rec.Header().Get("Anthropic-Ratelimit-Unified-Status"); got != "rejected" {
		t.Errorf("rate-limit headers were dropped: %q", got)
	}
	if res.Status != http.StatusTooManyRequests {
		t.Errorf("Result.Status = %d, want 429 so it is recorded as what it was", res.Status)
	}
	if res.Err != nil {
		t.Errorf("Err = %v, want nil: the request was answered, not failed", res.Err)
	}
}

// A refusal with nothing behind it — no upstream reached at all — still has to
// surface as an error, or the caller has nothing to report.
func TestNoAccountStillErrors(t *testing.T) {
	relay := &Relay{Pool: &emptyPool{}, Client: http.DefaultClient}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	res := relay.Do(rec, req, "anthropic", "/v1/messages", []byte("{}"), Prologue{})

	if res.Err == nil {
		t.Fatal("no accounts reported as success")
	}
	if res.Status != 0 {
		t.Errorf("Status = %d, want 0 so the caller writes its own error", res.Status)
	}
}

type emptyPool struct{}

func (emptyPool) Acquire(context.Context, string, map[string]bool) (pool.Lease, error) {
	return pool.Lease{}, pool.ErrNoAccounts
}
func (emptyPool) ReportFailure(string, pool.FailureKind, string) {}
func (emptyPool) ReportSuccess(string)                           {}
func (emptyPool) Refresh(context.Context, string) error          { return nil }
