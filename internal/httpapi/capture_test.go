package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"claudication/internal/service/capture"
	"claudication/internal/store"
)

// Recording a client's content is its own switch: off, nothing is kept; on,
// a request is kept as the client sent it, as the relay sent it upstream —
// here translated, Responses to Messages — and both answers, with every
// credential taken out, tied to its request log row and exported as HAR.
// Another client's requests are not recorded, and switching it off stops it.
func TestRecordingAClientsContent(t *testing.T) {
	srv, st, base, _, claude, _ := twoUpstreams(t)
	abase, ck := adminAPI(t, srv)
	bind := map[string]store.Binding{"large": {Model: "claude-sonnet-5"}}
	c, key := newClient(t, st, "watched", "custom", bind)
	_, other := newClient(t, st, "other", "custom", bind)
	turn := `{"model":"large","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"what is in auth.go?"}]}]}`

	ask := func(key string) {
		t.Helper()
		resp, out := send(t, base+"/v1/responses", key, "mytool/1.0", turn, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", resp.StatusCode, out)
		}
	}
	held := func() []capture.Summary {
		t.Helper()
		status, body := call(t, abase, http.MethodGet, "/admin/captures", "", ck)
		if status != http.StatusOK {
			t.Fatalf("captures: %d %s", status, body)
		}
		var got struct {
			Captures []capture.Summary `json:"captures"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		return got.Captures
	}

	ask(key)
	if n := len(held()); n != 0 {
		t.Fatalf("%d recorded with recording off", n)
	}

	status, body := call(t, abase, http.MethodPost, "/admin/clients/"+c.ID+"/capture", `{"minutes":5}`, ck)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"recording_until"`)) {
		t.Fatalf("capture on: %d %s", status, body)
	}
	ask(key)
	ask(other)
	got := held()
	if len(got) != 1 || got[0].KeyID != c.ID {
		t.Fatalf("held = %+v, want one exchange of the watched client", got)
	}
	x := got[0]
	if x.Status != http.StatusOK || x.Attempts != 1 || x.Provider != "anthropic" {
		t.Errorf("summary = %+v", x)
	}

	status, body = call(t, abase, http.MethodGet, fmt.Sprintf("/admin/captures/%d", x.ID), "", ck)
	if status != http.StatusOK {
		t.Fatalf("capture: %d %s", status, body)
	}
	if bytes.Contains(body, []byte(key)) {
		t.Error("the recording carries the client's key")
	}
	var ex struct {
		Client   legView   `json:"client"`
		Upstream []legView `json:"upstream"`
	}
	if err := json.Unmarshal(body, &ex); err != nil {
		t.Fatal(err)
	}
	if ex.Client.Dialect != "openai" || !strings.Contains(ex.Client.Request.Body.Text, "what is in auth.go?") {
		t.Errorf("client request = %+v", ex.Client)
	}
	if ex.Client.Response == nil || !strings.Contains(ex.Client.Response.Body.Text, "response.completed") {
		t.Errorf("client answer = %+v, want the Responses stream the client got", ex.Client.Response)
	}
	if len(ex.Upstream) != 1 {
		t.Fatalf("%d upstream legs, want 1", len(ex.Upstream))
	}
	up := ex.Upstream[0]
	turns := claude.turns()
	if up.Dialect != "anthropic" || up.Request.Body.Text != string(turns[len(turns)-1].body) {
		t.Errorf("upstream request = %q, want the bytes Claude was sent", up.Request.Body.Text)
	}
	if up.Response == nil || !strings.Contains(up.Response.Body.Text, "message_stop") {
		t.Errorf("upstream answer = %+v, want Claude's own stream", up.Response)
	}
	for _, h := range append(append([][2]string{}, ex.Client.Request.Header...), up.Request.Header...) {
		switch strings.ToLower(h[0]) {
		case "authorization", "x-api-key":
			if h[1] != capture.Redacted {
				t.Errorf("%s recorded as %q", h[0], h[1])
			}
		}
	}

	// The log's row says it has a recording.
	status, body = call(t, abase, http.MethodGet, "/admin/requests?limit=5", "", ck)
	if status != http.StatusOK || !bytes.Contains(body, []byte(fmt.Sprintf(`"capture":%d`, x.ID))) {
		t.Errorf("requests: %d %s, want a row naming capture %d", status, body, x.ID)
	}

	status, body = call(t, abase, http.MethodGet, fmt.Sprintf("/admin/captures/%d/har", x.ID), "", ck)
	var har struct {
		Log struct {
			Entries []json.RawMessage `json:"entries"`
		} `json:"log"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &har) != nil || len(har.Log.Entries) != 2 {
		t.Errorf("har: %d, %d entries, want the client's leg and Claude's", status, len(har.Log.Entries))
	}

	if status, body := call(t, abase, http.MethodDelete, "/admin/clients/"+c.ID+"/capture", "", ck); status != http.StatusOK ||
		bytes.Contains(body, []byte(`"recording_until"`)) {
		t.Fatalf("capture off: %d %s", status, body)
	}
	ask(key)
	if n := len(held()); n != 1 {
		t.Errorf("%d held after switching off, want the one from before", n)
	}
}

type legView struct {
	Dialect  string       `json:"dialect"`
	Request  messageView  `json:"request"`
	Response *messageView `json:"response"`
}

type messageView struct {
	Header [][2]string `json:"header"`
	Body   struct {
		Text string `json:"text"`
	} `json:"body"`
}
