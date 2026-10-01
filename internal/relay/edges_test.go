package relay

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"claudication/internal/provider/anthropic"
	"claudication/internal/relay/passes"
	"claudication/internal/request"
)

func relayTo(srv *httptest.Server, p AccountPool) *Relay {
	return &Relay{Wire: anthropic.Provider{},
		Pool:    p,
		Client:  srv.Client(),
		Log:     slog.New(slog.DiscardHandler),
		BaseURL: srv.URL,
	}
}

// The deadlines are outer bounds the client's own watchdogs sit well inside;
// one shorter than the client's 600 s would cut off requests it was still
// waiting on.
func TestTimeoutIsAnOuterBound(t *testing.T) {
	if Timeout(true) != 30*time.Minute || Timeout(false) != 10*time.Minute {
		t.Errorf("Timeout = %s / %s", Timeout(true), Timeout(false))
	}
	if Timeout(false) < 600*time.Second {
		t.Error("the non-streaming deadline is inside the client's own 600 s timeout")
	}
}

// A refusal the client receives verbatim, however long, while the copy kept
// for the log and usage row is bounded: an upstream answering with a page of
// HTML must not become a megabyte per row.
func TestALongRefusalIsBoundedOnlyInTheLog(t *testing.T) {
	long := strings.Repeat("x", 6000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("  " + long + "  "))
	}))
	defer srv.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	res := relayTo(srv, &recordingPool{}).Do(rec, req, "anthropic", "/v1/messages", []byte(`{}`), request.Prologue{})
	if rec.Body.String() != "  "+long+"  " {
		t.Errorf("client got %d bytes, want the upstream's body byte for byte", rec.Body.Len())
	}
	// The first 4096 bytes, then trimmed: the two leading spaces go.
	if len(res.UpstreamError) != 4094 || strings.TrimLeft(res.UpstreamError, "x") != "" {
		t.Errorf("UpstreamError is %d bytes, want the first 4096 trimmed", len(res.UpstreamError))
	}
}

// The same bound on the replay path, where every account refused and the last
// refusal is the answer.
func TestAReplayedRefusalIsBoundedOnlyInTheLog(t *testing.T) {
	long := strings.Repeat("y", 5000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.Header().Set("Connection", "close")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(long))
	}))
	defer srv.Close()

	p := &recordingPool{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	res := relayTo(srv, p).Do(rec, req, "anthropic", "/v1/messages", []byte(`{}`), request.Prologue{})
	if rec.Code != http.StatusTooManyRequests || rec.Body.String() != long {
		t.Errorf("client got %d and %d bytes, want the 429 verbatim", rec.Code, rec.Body.Len())
	}
	if rec.Header().Get("Retry-After") != "7" {
		t.Error("Retry-After was lost; it is what the client backs off on")
	}
	if rec.Header().Get("Connection") != "" {
		t.Error("a hop-by-hop header was replayed to the client")
	}
	if len(res.UpstreamError) != 4096 {
		t.Errorf("UpstreamError is %d bytes, want 4096", len(res.UpstreamError))
	}
	if p.failures != 1 || res.Status != http.StatusTooManyRequests {
		t.Errorf("failures=%d status=%d", p.failures, res.Status)
	}
}

// An upstream that cannot be reached is the account's (or the network's)
// problem and worth failing over; a client that hung up is nobody's, and must
// not count against the account it happened to be on.
func TestNetworkFailureCountsAgainstTheAccountButCancellationDoesNot(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close() // connection refused from here on

	p := &recordingPool{}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	res := relayTo(srv, p).Do(httptest.NewRecorder(), req, "anthropic", "/v1/messages", []byte(`{}`), request.Prologue{})
	if res.Err == nil || p.failures != 1 || res.Attempts != 2 {
		t.Errorf("err=%v failures=%d attempts=%d, want the account reported and the next attempt finding none",
			res.Err, p.failures, res.Attempts)
	}

	p = &recordingPool{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
	res = relayTo(srv, p).Do(httptest.NewRecorder(), req, "anthropic", "/v1/messages", []byte(`{}`), request.Prologue{})
	if res.Err == nil || p.failures != 0 || res.Attempts != 1 {
		t.Errorf("err=%v failures=%d attempts=%d, want a cancelled caller blamed on nobody", res.Err, p.failures, res.Attempts)
	}
}

// When a pass rewrote tool names on the way up, the names coming back must be
// the client's own again — even when the upstream splits one across reads —
// or the client cannot match a call to a tool it declared.
func TestRewrittenToolNamesAreRestoredOnTheWayBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		for _, chunk := range []string{
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"name\":\"todo",
			"write_\"}}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\",\"name\":\"todowrite_\"}",
		} {
			_, _ = w.Write([]byte(chunk))
			_ = rc.Flush()
		}
	}))
	defer srv.Close()

	r := relayTo(srv, &recordingPool{})
	r.Passes = passes.Pipeline{{
		Name: "names",
		Apply: func(body []byte, st *passes.State) []byte {
			st.Names = map[string]string{"todowrite_": "todowrite"}
			return body
		},
	}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	res := r.Do(rec, req, "anthropic", "/v1/messages", []byte(`{"stream":true}`), request.Prologue{})
	got := rec.Body.String()
	if strings.Contains(got, "todowrite_") || strings.Count(got, `"name":"todowrite"`) != 2 {
		t.Errorf("client got %q, want both names restored, the unterminated tail included", got)
	}
	if res.BytesOut != int64(len(got)) {
		t.Errorf("BytesOut = %d, client got %d", res.BytesOut, len(got))
	}
}

// Only a plain 200 event stream is held. A refusal must reach the client at
// once, and a compressed stream cannot be read to decide anything about it.
func TestOnlyAPlainStreamIsHeld(t *testing.T) {
	cases := []struct {
		status   int
		ct, enc  string
		holdable bool
	}{
		{200, "text/event-stream", "", true},
		{200, "text/event-stream; charset=utf-8", "identity", true},
		{200, "text/event-stream", "gzip", false},
		{200, "application/json", "", false},
		{429, "text/event-stream", "", false},
	}
	for _, c := range cases {
		resp := &http.Response{StatusCode: c.status, Header: http.Header{}}
		resp.Header.Set("Content-Type", c.ct)
		if c.enc != "" {
			resp.Header.Set("Content-Encoding", c.enc)
		}
		if got := holdable(resp); got != c.holdable {
			t.Errorf("holdable(%d %s %s) = %v", c.status, c.ct, c.enc, got)
		}
	}

	// End to end: with a stall timeout far shorter than the server's pause,
	// a refusal is still answered on the first attempt, not held and retried.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error"}`))
	}))
	defer srv.Close()
	r := relayTo(srv, &recordingPool{})
	r.StallTimeout = 10 * time.Millisecond
	res, body := doStream(r)
	if res.Stalls != 0 || res.Attempts != 1 || body != `{"type":"error"}` {
		t.Errorf("stalls=%d attempts=%d body=%q", res.Stalls, res.Attempts, body)
	}
}

// A stream that ends before any content is an answer, not a stall: holding it
// must hand on every byte and then the end, not wait for more.
func TestAStreamThatEndsBeforeContentIsRelayedAsIs(t *testing.T) {
	srv, attempts := stallServer(t, func(int) []string { return []string{sseStart, ssePing} })
	res, body := doStream(stallRelay(srv, &recordingPool{}, time.Minute))
	if attempts() != 1 || res.Stalls != 0 {
		t.Fatalf("attempts=%d stalls=%d", attempts(), res.Stalls)
	}
	if body != sseStart+ssePing || res.Err != nil {
		t.Errorf("body=%q err=%v", body, res.Err)
	}
}

// The outcome names go into a warning an operator reads.
func TestHoldOutcomeNames(t *testing.T) {
	for o, want := range map[holdOutcome]string{holdProgressed: "progressed", holdStalled: "stalled", holdErrored: "errored"} {
		if o.String() != want {
			t.Errorf("%d.String() = %q, want %q", o, o.String(), want)
		}
	}
}
