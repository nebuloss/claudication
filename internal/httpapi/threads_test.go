package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"claudication/internal/store"
)

// A session that runs a subagent beside its main thread, on the same model,
// interleaved — as Claude Code 2.1.317 was measured to (admin, chats.go) —
// reads as two threads, each turn counted against the one before it on its
// own thread, and the main one first.
func TestAChatReadsAsThreads(t *testing.T) {
	srv, st, base, _, _, _ := twoUpstreams(t)
	abase, ck := adminAPI(t, srv)
	c, key := newClient(t, st, "watched", "custom", map[string]store.Binding{"large": {Model: "claude-sonnet-5"}})
	srv.capture.Record(c.ID, time.Minute)
	chat := map[string]string{"X-Session-Id": "chat-t"}
	main := `"system":"You are the main agent.","messages":[{"role":"user","content":"main question"}`
	side := `"system":"You are a subagent.","messages":[{"role":"user","content":"side task"}`
	for _, body := range []string{
		main + `]`,
		side + `]`,
		side + `,{"role":"assistant","content":"hi"},{"role":"user","content":"side step"}]`,
		main + `,{"role":"assistant","content":"hi"},{"role":"user","content":"main follow-up"}]`,
	} {
		turn := `{"model":"large","max_tokens":10,"stream":true,` + body + `}`
		if resp, out := send(t, base+"/v1/messages", key, "mytool/1.0", turn, chat); resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", resp.StatusCode, out)
		}
	}

	status, body := call(t, abase, http.MethodGet, "/admin/chats/chat-t/transcript", "", ck)
	var got struct {
		Threads []struct {
			ID       int    `json:"id"`
			Label    string `json:"label"`
			Main     bool   `json:"main"`
			Requests int    `json:"requests"`
		} `json:"threads"`
		Turns []struct {
			Thread    int               `json:"thread"`
			Offset    int               `json:"offset"`
			Restarted bool              `json:"restarted"`
			Added     []json.RawMessage `json:"added"`
		} `json:"turns"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &got) != nil {
		t.Fatalf("transcript: %d %s", status, body)
	}
	if len(got.Threads) != 2 || !got.Threads[0].Main || got.Threads[1].Main ||
		got.Threads[0].Requests != 2 || got.Threads[1].Requests != 2 || got.Threads[1].Label != "Side thread 1" {
		t.Fatalf("threads = %+v", got.Threads)
	}
	want := []struct{ thread, offset int }{{1, 0}, {2, 0}, {2, 2}, {1, 2}}
	if len(got.Turns) != len(want) {
		t.Fatalf("%d turns", len(got.Turns))
	}
	for i, w := range want {
		tr := got.Turns[i]
		if tr.Thread != w.thread || tr.Offset != w.offset || tr.Restarted {
			t.Errorf("turn %d = thread %d offset %d restarted %v, want thread %d offset %d", i+1,
				tr.Thread, tr.Offset, tr.Restarted, w.thread, w.offset)
		}
	}
	if !bytes.Contains(got.Turns[3].Added[0], []byte("main follow-up")) {
		t.Errorf("the main thread's second turn added %s", got.Turns[3].Added)
	}

	// The threads alone, with which each request is on.
	status, body = call(t, abase, http.MethodGet, "/admin/chats/chat-t/transcript?threads=only", "", ck)
	var only struct {
		Threads  []json.RawMessage `json:"threads"`
		ThreadOf map[string]int    `json:"thread_of"`
		Turns    []json.RawMessage `json:"turns"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &only) != nil || len(only.Threads) != 2 ||
		len(only.ThreadOf) != 4 || only.Turns != nil {
		t.Errorf("threads only: %d %s", status, body)
	}
}
