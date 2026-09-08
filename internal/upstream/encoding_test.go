package upstream

import (
	"compress/gzip"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"claudication/internal/pool"
	"claudication/internal/store"
)

// A client asking for gzip must not be able to blind the gateway.
//
// This is the shape of the bug: Go's transport decompresses transparently only
// when it set Accept-Encoding itself. A client that sends its own — curl
// --compressed, python-requests, most SDKs — used to have that header
// forwarded, so the response arrived still compressed and everything that
// reads the body read gzip. Usage came out as zero tokens, and the SSE scanner
// could not see an `event: error`, so a stream that failed halfway was
// recorded as a success and the account that failed it was credited.
func TestAClientAskingForGzipStillGetsAccountedFor(t *testing.T) {
	const stream = "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":11,"output_tokens":0}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":7}}` + "\n\n"

	var sawAcceptEncoding string
	upstreamSrv := httptest.NewServer(negotiating(&sawAcceptEncoding, stream))
	defer upstreamSrv.Close()

	r := &Relay{
		Pool:    &onePool{},
		Client:  upstreamSrv.Client(),
		Log:     slog.New(slog.DiscardHandler),
		BaseURL: upstreamSrv.URL,
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	// The header at the heart of it.
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()

	res := r.Do(rec, req, "anthropic", "/v1/messages", []byte("{}"), Prologue{})

	if sawAcceptEncoding != "identity" {
		t.Errorf("upstream saw Accept-Encoding %q, want identity: the relay has to read the body it relays",
			sawAcceptEncoding)
	}
	if res.Usage.InputTokens != 11 || res.Usage.OutputTokens != 7 {
		t.Errorf("usage = %+v, want 11 in / 7 out; a client's Accept-Encoding must not zero the accounting",
			res.Usage)
	}
	if rec.Body.String() != stream {
		t.Error("the client did not receive the stream verbatim")
	}
}

// And the half that matters more than the token counts: a stream that fails
// after its 200 must not be credited as a success.
func TestAFailedStreamIsNotCreditedWhenTheClientAsksForGzip(t *testing.T) {
	const stream = "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":0}}}` + "\n\n" +
		"event: error\n" +
		`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}` + "\n\n"

	upstreamSrv := httptest.NewServer(negotiating(nil, stream))
	defer upstreamSrv.Close()

	p := &recordingPool{}
	r := &Relay{
		Pool:    p,
		Client:  upstreamSrv.Client(),
		Log:     slog.New(slog.DiscardHandler),
		BaseURL: upstreamSrv.URL,
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Accept-Encoding", "gzip")
	res := r.Do(httptest.NewRecorder(), req, "anthropic", "/v1/messages", []byte("{}"), Prologue{})

	if res.StreamError == "" {
		t.Error("the mid-stream error was not seen")
	}
	if p.successes != 0 {
		t.Errorf("ReportSuccess called %d times for a stream that carried an error", p.successes)
	}
}

// If the upstream compresses anyway — which it may not, having been asked for
// identity — the relay must still pass the bytes through untouched, and must
// not pretend the body was empty and clean.
func TestAnUnexpectedlyCompressedBodyIsRelayedButNotCredited(t *testing.T) {
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		zw := gzip.NewWriter(w)
		_, _ = zw.Write([]byte("event: error\ndata: {\"type\":\"error\"}\n\n"))
		_ = zw.Close()
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

	if !res.Opaque {
		t.Error("a body the relay cannot read must be marked opaque")
	}
	if p.successes != 0 {
		t.Errorf("ReportSuccess called %d times for a body whose contents were never read", p.successes)
	}
	if rec.Body.Len() == 0 {
		t.Error("the bytes must still reach the client")
	}
}

// negotiating is a stub upstream that compresses when asked to, which is what
// makes these tests mean anything: a handler that always answers in plaintext
// would pass whether or not the relay forwards the client's Accept-Encoding.
// api.anthropic.com does honour the header. If sawAcceptEncoding is non-nil it
// records what arrived.
func negotiating(sawAcceptEncoding *string, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enc := r.Header.Get("Accept-Encoding")
		if sawAcceptEncoding != nil {
			*sawAcceptEncoding = enc
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(enc, "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			w.WriteHeader(http.StatusOK)
			zw := gzip.NewWriter(w)
			_, _ = zw.Write([]byte(body))
			_ = zw.Close()
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	})
}

// recordingPool counts what the relay reported, with one always-available
// account.
type recordingPool struct {
	successes int
	failures  int
	refreshes int
}

func (p *recordingPool) Acquire(_ context.Context, _ string, exclude map[string]bool) (pool.Lease, error) {
	if exclude["only"] {
		return pool.Lease{}, pool.ErrAllCoolingUp
	}
	return pool.Lease{
		Account:     store.Account{ID: "only", Email: "only@example.com"},
		AccessToken: "token",
	}, nil
}

func (p *recordingPool) ReportFailure(string, pool.FailureKind, string) { p.failures++ }
func (p *recordingPool) ReportSuccess(string)                           { p.successes++ }
func (p *recordingPool) Refresh(context.Context, string) error          { p.refreshes++; return nil }
