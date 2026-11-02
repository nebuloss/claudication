package httpapi

import (
	"bytes"
	"encoding/json"
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
}
