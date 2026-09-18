package httpapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"claudication/internal/store"
)

// anthropicRequest is the smallest thing a Claude-dialect client sends.
const anthropicRequest = `{"model":"claude-sonnet-5","max_tokens":16,"stream":true,
"messages":[{"role":"user","content":"say hi"}]}`

// postHeaders sends a request with headers the caller chooses, which is the
// whole point here: the conversation id only ever arrives in one.
func postHeaders(t *testing.T, url, key, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Api-Key", key)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// waitForEvents returns the newest n usage rows, waiting for them to arrive.
//
// The wait is not paranoia and it is not a sleep standing in for a fix. Usage
// is recorded inside the handler, but *after* the last byte of the answer has
// gone to the client — so a client that has finished reading the body can, and
// does, get there before the handler has finished the request. Asserting
// straight after io.ReadAll passes or fails on which goroutine wins.
//
// It showed up as three of four cases failing while the Codex one passed,
// which looked like a bug in the Anthropic path and was nothing of the sort:
// Codex's sink writes its terminal event during Close, later in the handler,
// so it happened to win a race the others lost.
func waitForEvents(t *testing.T, st *store.Store, n int) []store.UsageEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		events, _, err := st.RecentUsage(context.Background(), 20, store.UsageCursor{}, store.RequestFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(events) >= n {
			return events
		}
		if time.Now().After(deadline) {
			t.Fatalf("recorded %d usage events, want %d", len(events), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// lastEvent is the usage row the request just wrote.
func lastEvent(t *testing.T, st *store.Store) store.UsageEvent {
	t.Helper()
	return waitForEvents(t, st, 1)[0]
}

// okUpstream answers every request with one complete Anthropic stream.
func okUpstream() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, anthropicStream)
	}
}

// Each client names its chat in a different header, and every one of them has
// to land in the same column. The values are real, captured on 2026-09-17.
func TestConversationIDIsRecordedForEveryClient(t *testing.T) {
	for _, c := range []struct {
		name    string
		path    string
		body    string
		headers map[string]string
		want    string
		client  string
	}{{
		name: "Claude Code sends its own header",
		path: "/v1/messages",
		body: anthropicRequest,
		headers: map[string]string{
			"X-Claude-Code-Session-Id": "04c27413-cc40-4b20-be7c-e33c5b61ff3d",
			"User-Agent":               "claude-cli/2.1.274 (external, sdk-cli)",
		},
		want:   "04c27413-cc40-4b20-be7c-e33c5b61ff3d",
		client: "Claude Code",
	}, {
		name: "opencode and crush send x-session-id",
		path: "/v1/messages",
		body: anthropicRequest,
		headers: map[string]string{
			"X-Session-Id":       "ses_f509900ffffeu4d2HmSTq8OoXV",
			"X-Session-Affinity": "ses_f509900ffffeu4d2HmSTq8OoXV",
			"User-Agent":         "opencode/1.17.13 ai-sdk/provider-utils/4.0.27 runtime/bun/1.3.14",
		},
		want:   "ses_f509900ffffeu4d2HmSTq8OoXV",
		client: "opencode",
	}, {
		// The header Codex sends is one the relay strips before going
		// upstream, so this only passes if it is read from the caller's own
		// request first. It is the ordering in inference that is under test.
		name: "Codex sends session_id, which is stripped upstream",
		path: "/v1/responses",
		body: codexRequest,
		headers: map[string]string{
			"Session_id": "01931f2a-6c05-73b2-9f41-b7d40e8c4e11",
			"User-Agent": "codex_cli_rs/0.54.0",
		},
		want:   "01931f2a-6c05-73b2-9f41-b7d40e8c4e11",
		client: "Codex",
	}, {
		// No header at all: the older Claude Code path, where the only copy is
		// inside metadata.user_id as a JSON string holding JSON.
		name: "a body-only session id is still found",
		path: "/v1/messages",
		body: `{"model":"claude-sonnet-5","max_tokens":16,"stream":true,` +
			`"messages":[{"role":"user","content":"hi"}],` +
			`"metadata":{"user_id":"{\"device_id\":\"d\",\"session_id\":\"9d0e11b7-2f84-4a11-bc3e-41f6c3a0a027\"}"}}`,
		headers: map[string]string{"User-Agent": "claude-cli/2.1.274"},
		want:    "9d0e11b7-2f84-4a11-bc3e-41f6c3a0a027",
		client:  "Claude Code",
	}} {
		t.Run(c.name, func(t *testing.T) {
			srv, st, _ := newTestServer(t)
			base, key := relayTo(t, srv, st, okUpstream())

			resp := postHeaders(t, base+c.path, key, c.body, c.headers)
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
			}

			got := lastEvent(t, st)
			if got.ConversationID != c.want {
				t.Errorf("conversation = %q, want %q", got.ConversationID, c.want)
			}
			if got.Client != c.client {
				t.Errorf("client = %q, want %q", got.Client, c.client)
			}
		})
	}
}

// A client that names no conversation leaves the column empty. Empty is a real
// answer — it is how an operator finds out a client needs configuring — and it
// must never be filled with something invented.
func TestNoSessionIDLeavesTheChatEmpty(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, key := relayTo(t, srv, st, okUpstream())

	resp := postHeaders(t, base+"/v1/messages", key, anthropicRequest,
		map[string]string{"User-Agent": "curl/8.5.0"})
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	got := lastEvent(t, st)
	if got.ConversationID != "" {
		t.Errorf("conversation = %q, want empty rather than a guess", got.ConversationID)
	}
	if got.Client != "curl" {
		t.Errorf("client = %q, want the unknown client named from its own token", got.Client)
	}
}

// Two turns of one conversation are two rows that group. Without this the
// column is recorded and still useless.
func TestTurnsOfOneChatShareTheirID(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, key := relayTo(t, srv, st, okUpstream())

	headers := map[string]string{
		"X-Claude-Code-Session-Id": "04c27413-cc40-4b20-be7c-e33c5b61ff3d",
		"User-Agent":               "claude-cli/2.1.274",
	}
	for range 2 {
		resp := postHeaders(t, base+"/v1/messages", key, anthropicRequest, headers)
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
	}

	events := waitForEvents(t, st, 2)
	if len(events) != 2 {
		t.Fatalf("recorded %d events, want 2", len(events))
	}
	if events[0].ConversationID != events[1].ConversationID {
		t.Errorf("ids differ across turns: %q vs %q",
			events[0].ConversationID, events[1].ConversationID)
	}
}
