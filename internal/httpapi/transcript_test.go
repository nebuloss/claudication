package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"claudication/internal/store"
)

// A recorded chat reads as a transcript: each turn shows only what it added
// — the first its opening message, the next the new user message after the
// answer before it — with each turn's answer, and the chat's rows name their
// recordings.
func TestRecordedChatReadsAsATranscript(t *testing.T) {
	srv, st, base, _, _, _ := twoUpstreams(t)
	abase, ck := adminAPI(t, srv)
	c, key := newClient(t, st, "watched", "custom", map[string]store.Binding{"large": {Model: "claude-sonnet-5"}})
	srv.capture.Record(c.ID, time.Minute)
	chat := map[string]string{"X-Session-Id": "chat-1"}

	turns := []string{
		`{"model":"large","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"first question"}]}`,
		`{"model":"large","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"first question"},` +
			`{"role":"assistant","content":"hi"},{"role":"user","content":"second question"}]}`,
	}
	for _, turn := range turns {
		if resp, out := send(t, base+"/v1/messages", key, "mytool/1.0", turn, chat); resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", resp.StatusCode, out)
		}
	}

	status, body := call(t, abase, http.MethodGet, "/admin/chats/chat-1", "", ck)
	if status != http.StatusOK || bytes.Count(body, []byte(`"capture":`)) != 2 {
		t.Errorf("chat: %d %s, want both rows naming their recording", status, body)
	}
	status, body = call(t, abase, http.MethodGet, "/admin/chats/chat-1/transcript", "", ck)
	var got struct {
		Turns []struct {
			Offset int               `json:"offset"`
			Added  []json.RawMessage `json:"added"`
			Answer json.RawMessage   `json:"answer"`
		} `json:"turns"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &got) != nil || len(got.Turns) != 2 {
		t.Fatalf("transcript: %d %s", status, body)
	}
	first, second := got.Turns[0], got.Turns[1]
	if first.Offset != 0 || len(first.Added) != 1 || !bytes.Contains(first.Added[0], []byte("first question")) {
		t.Errorf("first turn = offset %d, %s", first.Offset, first.Added)
	}
	if second.Offset != 2 || len(second.Added) != 1 || !bytes.Contains(second.Added[0], []byte("second question")) {
		t.Errorf("second turn = offset %d, %s, want only the new question", second.Offset, second.Added)
	}
	for i, turn := range got.Turns {
		if !bytes.Contains(turn.Answer, []byte(`"text":"hi"`)) {
			t.Errorf("turn %d answer = %s", i+1, turn.Answer)
		}
	}

	// A window of the chat: only its turns come back, numbered as the whole
	// chat numbers them — the turn before it still counted.
	var rows struct {
		Requests []struct {
			ID int64 `json:"id"`
		} `json:"requests"`
	}
	_, body = call(t, abase, http.MethodGet, "/admin/chats/chat-1", "", ck)
	if err := json.Unmarshal(body, &rows); err != nil || len(rows.Requests) != 2 {
		t.Fatalf("chat rows: %v %s", err, body)
	}
	last := rows.Requests[1].ID

	// One request by its id, as a link to its dialog reads it: the log's row,
	// its recording named.
	status, body = call(t, abase, http.MethodGet, fmt.Sprintf("/admin/requests/%d", last), "", ck)
	if status != http.StatusOK || !bytes.Contains(body, []byte(fmt.Sprintf(`"id":%d`, last))) ||
		!bytes.Contains(body, []byte(`"capture":`)) || !bytes.Contains(body, []byte(`"conversation_id":"chat-1"`)) {
		t.Errorf("request %d: %d %s", last, status, body)
	}
	for _, missing := range []string{"999999", "nope"} {
		if status, _ := call(t, abase, http.MethodGet, "/admin/requests/"+missing, "", ck); status != http.StatusNotFound {
			t.Errorf("request %s: %d, want 404", missing, status)
		}
	}
	for range 2 { // the second read takes the first turn's count from the cache
		status, body = call(t, abase, http.MethodGet, fmt.Sprintf("/admin/chats/chat-1/transcript?from=%d", last), "", ck)
		got.Turns = nil
		if status != http.StatusOK || json.Unmarshal(body, &got) != nil || len(got.Turns) != 1 {
			t.Fatalf("window: %d %s", status, body)
		}
		if w := got.Turns[0]; w.Offset != 2 || len(w.Added) != 1 || !bytes.Contains(w.Added[0], []byte("second question")) {
			t.Errorf("window's turn = offset %d, %s", w.Offset, w.Added)
		}
	}
}
