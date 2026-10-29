package relay

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"claudication/internal/provider/anthropic"
	"claudication/internal/request"
)

const (
	sseStart = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n"
	ssePing  = "event: ping\ndata: {\"type\": \"ping\"}\n\n"
	sseBlock = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"
	sseDelta = "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"
	sseEnd   = "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	sseOverloaded = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
)

// pause, as a chunk, is a silence of 250 ms in the middle of a stream.
const pause = "\x00pause"

// stallServer answers attempt n with script(n). Each script is a list of
// chunks; a chunk of "" means "go silent until the request is abandoned".
func stallServer(t *testing.T, script func(n int) []string) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		for _, chunk := range script(n) {
			if chunk == pause {
				time.Sleep(250 * time.Millisecond)
				continue
			}
			if chunk == "" {
				// What the upstream did on 2026-10-01: accepted, opened, and
				// then nothing. Held until the gateway lets go of it, which
				// is also the check that an abandoned attempt is torn down.
				<-r.Context().Done()
				return
			}
			_, _ = w.Write([]byte(chunk))
			_ = rc.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() int { mu.Lock(); defer mu.Unlock(); return attempts }
}

func stallRelay(srv *httptest.Server, p AccountPool, timeout time.Duration) *Relay {
	return &Relay{Wire: anthropic.Provider{},
		Pool:         p,
		Client:       srv.Client(),
		Log:          slog.New(slog.DiscardHandler),
		BaseURL:      srv.URL,
		StallTimeout: timeout,
	}
}

func doStream(r *Relay) (Result, string) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	res := r.Do(rec, req, "/v1/messages", []byte(`{"stream":true}`), request.Prologue{})
	return res, rec.Body.String()
}

// The case this exists for: a stream that opens and never produces anything.
// It is sent again, on the same account — there is only one — and the client
// receives the second answer alone, with no trace of the first.
func TestAStreamThatNeverStartsIsSentAgain(t *testing.T) {
	srv, attempts := stallServer(t, func(n int) []string {
		if n == 1 {
			return []string{sseStart, ssePing, sseBlock, ""}
		}
		return []string{sseStart, sseBlock, sseDelta, sseEnd}
	})
	p := &recordingPool{}
	res, body := doStream(stallRelay(srv, p, 150*time.Millisecond))

	if attempts() != 2 || res.Stalls != 1 || res.Attempts != 2 {
		t.Fatalf("attempts=%d stalls=%d res.Attempts=%d, want 2/1/2", attempts(), res.Stalls, res.Attempts)
	}
	want := sseStart + sseBlock + sseDelta + sseEnd
	if body != want {
		t.Errorf("client received\n%q\nwant only the second answer\n%q", body, want)
	}
	if n := strings.Count(body, "event: message_start"); n != 1 {
		t.Errorf("message_start sent %d times", n)
	}
	if p.failures != 0 {
		t.Errorf("failures = %d: a stall must not cool down the account that will serve the retry", p.failures)
	}
	if res.Usage.OutputTokens != 7 {
		t.Errorf("usage read from the wrong attempt: %+v", res.Usage)
	}
	if res.FirstContent < 150*time.Millisecond {
		t.Errorf("first token at %s: the client waited through the stalled attempt too", res.FirstContent)
	}
}

// The 07:56 case: the upstream sat silent and then said it was overloaded.
// Before any content that is as retryable as silence.
func TestAnErrorBeforeContentIsSentAgain(t *testing.T) {
	srv, attempts := stallServer(t, func(n int) []string {
		if n == 1 {
			return []string{sseStart, sseOverloaded}
		}
		return []string{sseStart, sseDelta, sseEnd}
	})
	res, body := doStream(stallRelay(srv, &recordingPool{}, time.Second))

	if attempts() != 2 || res.Stalls != 1 {
		t.Fatalf("attempts=%d stalls=%d, want 2/1", attempts(), res.Stalls)
	}
	if strings.Contains(body, "overloaded_error") || res.StreamError != "" {
		t.Errorf("the abandoned attempt's error reached the client: %q / %q", body, res.StreamError)
	}
}

// A healthy stream is the same byte copy it always was, with the time to
// first token recorded.
func TestAStreamThatStartsIsRelayedUnchanged(t *testing.T) {
	want := []string{sseStart, ssePing, sseBlock, sseDelta, sseEnd}
	srv, attempts := stallServer(t, func(int) []string { return want })
	res, body := doStream(stallRelay(srv, &recordingPool{}, time.Second))

	if attempts() != 1 || res.Stalls != 0 {
		t.Fatalf("attempts=%d stalls=%d, want 1/0", attempts(), res.Stalls)
	}
	if body != strings.Join(want, "") {
		t.Errorf("relayed\n%q\nwant\n%q", body, strings.Join(want, ""))
	}
	if res.FirstContent <= 0 {
		t.Error("first token not measured")
	}
}

// Once per request: if the retry stalls too, it is relayed as it is rather
// than abandoned again, so a request that genuinely takes long to start is
// sent at most twice.
func TestTheRetryIsNotHeld(t *testing.T) {
	srv, attempts := stallServer(t, func(n int) []string {
		if n == 1 {
			return []string{sseStart, ""}
		}
		// Slower to start than the timeout as well; a held retry would be
		// dropped a second time.
		return []string{sseStart, pause, sseDelta, sseEnd}
	})
	res, body := doStream(stallRelay(srv, &recordingPool{}, 100*time.Millisecond))

	if attempts() != 2 || res.Stalls != 1 {
		t.Fatalf("attempts=%d stalls=%d, want 2/1", attempts(), res.Stalls)
	}
	if body != sseStart+sseDelta+sseEnd {
		t.Errorf("the retry was not relayed whole: %q", body)
	}
}

// Off means off: the opening events go out the moment they arrive and a
// silent stream is simply waited on.
func TestStallTimeoutZeroRelaysAtOnce(t *testing.T) {
	srv, attempts := stallServer(t, func(int) []string { return []string{sseStart, sseDelta, sseEnd} })
	res, body := doStream(stallRelay(srv, &recordingPool{}, 0))
	if attempts() != 1 || res.Stalls != 0 || body != sseStart+sseDelta+sseEnd {
		t.Fatalf("attempts=%d stalls=%d body=%q", attempts(), res.Stalls, body)
	}
}

func TestClassifyHeld(t *testing.T) {
	for _, tc := range []struct {
		in      string
		decided bool
		want    holdOutcome
	}{
		{sseStart, false, holdProgressed},
		{sseStart + ssePing + sseBlock, false, holdProgressed},
		// One delta is not yet a flowing stream: that is exactly where the
		// stalls of 2026-10-01 afternoon stopped.
		{sseStart + sseDelta, false, holdProgressed},
		{sseStart + sseDelta + sseDelta, false, holdProgressed},
		{sseStart + sseDelta + sseDelta + sseDelta, true, holdProgressed},
		{sseStart + sseOverloaded, true, holdErrored},
		{sseStart + sseDelta + sseOverloaded, true, holdErrored},
		{sseStart + "event: message_delta\n", true, holdProgressed},
		{sseStart + sseDelta + "event: content_block_stop\n", true, holdProgressed},
		{"event: content_block_del", false, holdProgressed}, // partial line
	} {
		var sc heldScan
		got, decided := classifyHeld([]byte(tc.in), &sc, time.Now(), anthropic.Provider{}.Event)
		if decided != tc.decided || (decided && got != tc.want) {
			t.Errorf("%q: got %v decided=%v, want %v decided=%v", tc.in, got, decided, tc.want, tc.decided)
		}
	}
}

// The afternoon's stalls: one delta, then nothing. The opening is still held,
// so the stall is retried invisibly instead of reaching the client.
func TestAStreamThatStopsAfterOneDeltaIsSentAgain(t *testing.T) {
	defer func(g time.Duration) { progressGap = g }(progressGap)
	progressGap = 100 * time.Millisecond

	srv, attempts := stallServer(t, func(n int) []string {
		if n == 1 {
			return []string{sseStart, sseBlock, sseDelta, ""}
		}
		return []string{sseStart, sseBlock, sseDelta, sseDelta, sseDelta, sseEnd}
	})
	// The overall limit is long: it is the gap after the first delta that
	// must catch this one.
	res, body := doStream(stallRelay(srv, &recordingPool{}, time.Minute))
	if attempts() != 2 || res.Stalls != 1 {
		t.Fatalf("attempts=%d stalls=%d, want 2/1", attempts(), res.Stalls)
	}
	if want := sseStart + sseBlock + sseDelta + sseDelta + sseDelta + sseEnd; body != want {
		t.Errorf("client received\n%q\nwant only the second answer\n%q", body, want)
	}
}

// A reply short enough to finish in one delta is released the moment it
// settles, not held for more content that will never come.
func TestAShortCompleteAnswerIsNotHeld(t *testing.T) {
	defer func(g time.Duration) { progressGap = g }(progressGap)
	progressGap = time.Hour

	srv, attempts := stallServer(t, func(int) []string {
		return []string{sseStart, sseBlock, sseDelta, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n", sseEnd}
	})
	start := time.Now()
	res, _ := doStream(stallRelay(srv, &recordingPool{}, time.Hour))
	if attempts() != 1 || res.Stalls != 0 || res.Status != 200 {
		t.Fatalf("attempts=%d stalls=%d status=%d", attempts(), res.Stalls, res.Status)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("a short answer was held for %s", took)
	}
}

// A held stream is never silent: its headers go out with the first
// keepalive, comments follow while it holds, and the retry after a stall
// still reaches the client as the only answer — the comments are no events.
// Measured 2026-10-29: a proxy's 90 s read timeout cut crush's requests
// while the hold sent nothing at all.
func TestAHeldStreamKeepsTheConnectionAlive(t *testing.T) {
	defer func(d time.Duration) { keepaliveEvery = d }(keepaliveEvery)
	keepaliveEvery = 40 * time.Millisecond
	srv, attempts := stallServer(t, func(n int) []string {
		if n == 1 {
			return []string{sseStart, ssePing, ""}
		}
		return []string{sseStart, sseBlock, sseDelta, sseEnd}
	})
	res, body := doStream(stallRelay(srv, &recordingPool{}, 200*time.Millisecond))

	if attempts() != 2 || res.Stalls != 1 {
		t.Fatalf("attempts=%d stalls=%d, want 2/1", attempts(), res.Stalls)
	}
	if n := strings.Count(body, ": keepalive\n\n"); n < 2 {
		t.Errorf("%d keepalives while holding for 200 ms at 40 ms: %q", n, body)
	}
	answer := strings.ReplaceAll(body, ": keepalive\n\n", "")
	if want := sseStart + sseBlock + sseDelta + sseEnd; answer != want {
		t.Errorf("client received, comments aside,\n%q\nwant only the second answer\n%q", answer, want)
	}
}

// A retry that is refused once the status has gone out with a keepalive is
// said in the stream, as a Messages client hears an error mid-stream.
func TestARefusedRetryAfterAKeepaliveIsAnErrorEvent(t *testing.T) {
	defer func(d time.Duration) { keepaliveEvery = d }(keepaliveEvery)
	keepaliveEvery = 40 * time.Millisecond
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		attempt := n
		mu.Unlock()
		if attempt > 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseStart))
		http.NewResponseController(w).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	res := stallRelay(srv, &recordingPool{}, 150*time.Millisecond).Do(rec, req, "/v1/messages", []byte(`{"stream":true}`), request.Prologue{})

	if rec.Code != http.StatusOK || res.Status != http.StatusBadRequest {
		t.Fatalf("client status %d, result status %d: want the keepalive's 200 and the refusal recorded", rec.Code, res.Status)
	}
	want := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"prompt is too long\"}}\n\n"
	if !strings.HasSuffix(rec.Body.String(), want) {
		t.Errorf("stream ends\n%q\nwant the refusal as an error event", rec.Body.String())
	}
}

// goneWriter is a client that hangs up after its first write.
type goneWriter struct {
	*httptest.ResponseRecorder
	writes int
}

func (g *goneWriter) Write(b []byte) (int, error) {
	g.writes++
	if g.writes > 1 {
		return 0, errors.New("broken pipe")
	}
	return g.ResponseRecorder.Write(b)
}

// A client that hangs up mid-stream is the result's error, so the request is
// recorded as the client's failure and not as a success with no output.
func TestAClientThatHangsUpIsTheResultsError(t *testing.T) {
	srv, _ := stallServer(t, func(int) []string {
		return []string{sseStart, sseBlock, sseDelta, pause, sseDelta, sseEnd}
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	w := &goneWriter{ResponseRecorder: httptest.NewRecorder()}
	res := stallRelay(srv, &recordingPool{}, 0).Do(w, req, "/v1/messages", []byte(`{"stream":true}`), request.Prologue{})
	if !errors.Is(res.Err, ErrClientGone) {
		t.Errorf("res.Err = %v, want ErrClientGone", res.Err)
	}
}

// The keepalive is due on its own clock, not after a quiet spell: a thinking
// model pings every few seconds while it holds, and a client on a real
// connection gets the headers and a comment long before the first token.
func TestAKeepaliveGoesOutWhileTheUpstreamPings(t *testing.T) {
	// Restored after the servers below close (cleanups run last first), not
	// while the relay inside them still reads it.
	was := keepaliveEvery
	t.Cleanup(func() { keepaliveEvery = was })
	keepaliveEvery = 60 * time.Millisecond
	pings := []string{sseStart}
	for i := 0; i < 12; i++ {
		pings = append(pings, ssePing, "\x00short")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		for _, chunk := range append(pings, sseBlock, sseDelta, sseDelta, sseDelta, sseEnd) {
			if chunk == "\x00short" {
				time.Sleep(25 * time.Millisecond)
				continue
			}
			_, _ = w.Write([]byte(chunk))
			_ = rc.Flush()
		}
	}))
	t.Cleanup(upstream.Close)
	relay := stallRelay(upstream, &recordingPool{}, 5*time.Second)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relay.Do(w, r, "/v1/messages", []byte(`{"stream":true}`), request.Prologue{})
	}))
	t.Cleanup(front.Close)

	started := time.Now()
	resp, err := http.Post(front.URL+"/v1/messages", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	first := make([]byte, len(": keepalive"))
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		t.Fatal(err)
	}
	// The upstream pings for about 300 ms before its first token.
	if got := time.Since(started); string(first) != ": keepalive" || got > 250*time.Millisecond {
		t.Errorf("first bytes %q after %s: want a keepalive well before the first token", first, got)
	}
}

// An upstream that sits on its headers while the model thinks — measured
// 2026-10-29, a 504 from the operator's proxy at 90 s with not one byte
// from the gateway — gets the client a keepalive anyway, headers first, and
// then its answer as the only events in the stream.
func TestAKeepaliveGoesOutBeforeTheUpstreamAnswers(t *testing.T) {
	was := keepaliveEvery
	t.Cleanup(func() { keepaliveEvery = was })
	keepaliveEvery = 60 * time.Millisecond
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, chunk := range []string{sseStart, sseBlock, sseDelta, sseDelta, sseDelta, sseEnd} {
			_, _ = w.Write([]byte(chunk))
		}
	}))
	t.Cleanup(upstream.Close)
	relay := stallRelay(upstream, &recordingPool{}, 5*time.Second)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relay.Do(w, r, "/v1/messages", []byte(`{"stream":true}`), request.Prologue{Stream: true})
	}))
	t.Cleanup(front.Close)

	started := time.Now()
	resp, err := http.Post(front.URL+"/v1/messages", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := time.Since(started); got > 250*time.Millisecond {
		t.Errorf("headers after %s: the upstream took 300 ms, and the keepalive is due at 60", got)
	}
	body, _ := io.ReadAll(resp.Body)
	answer := strings.ReplaceAll(string(body), ": keepalive\n\n", "")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(string(body), ": keepalive") ||
		answer != sseStart+sseBlock+sseDelta+sseDelta+sseDelta+sseEnd {
		t.Errorf("status %d, body %q", resp.StatusCode, body)
	}
}
