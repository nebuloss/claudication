package upstream

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
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
	return &Relay{
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
	res := r.Do(rec, req, "anthropic", "/v1/messages", []byte(`{"stream":true}`), Prologue{})
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
	if strings.Count(body, "message_start") != 1 {
		t.Errorf("message_start sent %d times", strings.Count(body, "message_start"))
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
		{sseStart + sseDelta, true, holdProgressed},
		{sseStart + sseOverloaded, true, holdErrored},
		{sseStart + "event: message_delta\n", true, holdProgressed},
		{"event: content_block_del", false, holdProgressed}, // partial line
	} {
		got, decided, _ := classifyHeld([]byte(tc.in), 0)
		if decided != tc.decided || (decided && got != tc.want) {
			t.Errorf("%q: got %v decided=%v, want %v decided=%v", tc.in, got, decided, tc.want, tc.decided)
		}
	}
}
