package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"claudication/internal/store"
)

// With capture.retention-days set, what a client's recording holds outlives
// the process: a gateway started afresh on the same state — a restart, a
// deploy — serves the request's content, its log row says it has some, and
// its chat's transcript reads it, from the file alone.
func TestRecordingsOutliveARestart(t *testing.T) {
	srv, st, base, _, claude, _ := twoUpstreams(t)
	srv.cfg.Capture.RetentionDays = 7
	srv.keepRecordings(slog.New(slog.DiscardHandler))
	if srv.recordings == nil {
		t.Fatal("recordings were not kept on disk")
	}
	c, key := newClient(t, st, "watched", "custom", map[string]store.Binding{"large": {Model: "claude-sonnet-5"}})
	srv.capture.Record(c.ID, time.Minute)
	turn := `{"model":"large","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"kept across a restart"}]}`
	if resp, out := send(t, base+"/v1/messages", key, "mytool/1.0", turn, map[string]string{"X-Session-Id": "chat-r"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, out)
	}
	events := waitForEvents(t, st, 1)
	event := events[0].ID
	srv.closeRecordings()

	// Started afresh, on the same state: nothing held in memory.
	cfg := *srv.cfg
	again, err := New(cfg, slog.New(slog.DiscardHandler), st)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(again.closeRecordings)
	if again.recordings == nil {
		t.Fatal("the new process does not keep recordings")
	}
	abase, ck := adminAPI(t, again)

	status, body := call(t, abase, http.MethodGet, fmt.Sprintf("/admin/captures/%d", event), "", ck)
	if status != http.StatusOK {
		t.Fatalf("capture after a restart: %d %s", status, body)
	}
	var ex struct {
		Client   legView   `json:"client"`
		Upstream []legView `json:"upstream"`
	}
	if err := json.Unmarshal(body, &ex); err != nil || len(ex.Upstream) != 1 {
		t.Fatalf("%v, %s", err, body)
	}
	turns := claude.turns()
	if ex.Client.Request.Body.Text != turn || ex.Upstream[0].Request.Body.Text != string(turns[len(turns)-1].body) {
		t.Error("the bodies did not come back as they passed")
	}
	if ex.Upstream[0].Response == nil || !bytes.Contains([]byte(ex.Upstream[0].Response.Body.Text), []byte("message_stop")) {
		t.Errorf("upstream answer = %+v", ex.Upstream[0].Response)
	}

	status, body = call(t, abase, http.MethodGet, "/admin/requests?limit=5", "", ck)
	if status != http.StatusOK || !bytes.Contains(body, []byte(fmt.Sprintf(`"capture":%d`, event))) {
		t.Errorf("requests: %d %s, want the row naming its recording", status, body)
	}
	status, body = call(t, abase, http.MethodGet, "/admin/chats/chat-r/transcript", "", ck)
	if status != http.StatusOK || !bytes.Contains(body, []byte("kept across a restart")) {
		t.Errorf("transcript after a restart: %d %s", status, body)
	}
	status, body = call(t, abase, http.MethodGet, "/admin/captures", "", ck)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"stored":{`)) {
		t.Errorf("status: %d %s, want what is stored", status, body)
	}
}
