package upstream

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// opencode sends the environment line on every request, so this pass runs on
// all of its traffic. Only the system value may change; the transcript and
// everything else must come through as the client's own bytes.
func TestNormaliseSystemTouchesOnlySystem(t *testing.T) {
	messages := `"messages": [ {"role":"user",  "content":"keep   this <spacing> & é"} ]`
	body := `{"model":"m",` + messages + `,"system":[{"type":"text","text":"Is directory a git repo: yes"},{"type":"text","text":""}],"z":1}`
	out := NormaliseSystem([]byte(body))
	if !strings.Contains(string(out), messages) {
		t.Errorf("messages re-encoded:\n%s", out)
	}
	if !strings.HasPrefix(string(out), `{"model":"m",`) || !strings.HasSuffix(string(out), `,"z":1}`) {
		t.Errorf("key order or neighbours changed:\n%s", out)
	}
	var got struct {
		System []struct{ Text string } `json:"system"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.System) != 1 || got.System[0].Text != "Git repository: yes" {
		t.Errorf("system = %+v, want the reworded block alone", got.System)
	}
}

// Every block empty: the field goes, and the object stays well formed
// wherever it sat.
func TestNormaliseSystemRemovesAnAllEmptySystem(t *testing.T) {
	for _, body := range []string{
		`{"system":[{"type":"text","text":""}],"model":"m"}`,
		`{"model":"m","system":[{"type":"text","text":""}]}`,
		`{"model":"m", "system" : [{"type":"text","text":""}] , "x":1}`,
		`{"system":[{"type":"text","text":""}]}`,
	} {
		out := NormaliseSystem([]byte(body))
		var v map[string]any
		if err := json.Unmarshal(out, &v); err != nil {
			t.Errorf("%s -> invalid %s: %v", body, out, err)
			continue
		}
		if _, ok := v["system"]; ok {
			t.Errorf("%s -> system kept: %s", body, out)
		}
	}
}

// Only the message with the empty block is re-encoded; its neighbours are the
// client's bytes.
func TestDropEmptyMessageTextTouchesOnlyThatMessage(t *testing.T) {
	keep := `{"role":"user",  "content":[{"type":"text","text":"a  b"}]}`
	body := `{"messages":[` + keep + `,{"role":"assistant","content":[{"type":"text","text":""},{"type":"text","text":"x"}]},` + keep + `]}`
	out := DropEmptyMessageText([]byte(body))
	if strings.Count(string(out), keep) != 2 {
		t.Errorf("untouched messages re-encoded:\n%s", out)
	}
	if strings.Contains(string(out), `"text":""`) {
		t.Errorf("empty block survived:\n%s", out)
	}
	var v any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("invalid: %v\n%s", err, out)
	}
}

// The accepted mcp__ shape must not send a body through the rewrite; the
// refused mcp_ shape must.
func TestRefusedNameGateIgnoresDoubleUnderscore(t *testing.T) {
	for body, want := range map[string]bool{
		`{"tools":[{"name":"mcp__github__search"}]}`:                     false,
		`{"tools":[{"name":"mcp_github_search"}]}`:                       true,
		`{"tools":[{"name":"mcp__a"},{"name":"mcp_b"}]}`:                 true,
		`{"tools":[{"name":"Read"}],"messages":"mentions mcp_ in text"}`: false,
		`{"tools":[{"name":"todowrite"}]}`:                               true,
	} {
		if got := mayHoldRefusedName([]byte(body)); got != want {
			t.Errorf("%s: gate = %v, want %v", body, got, want)
		}
	}
}

// The request body lets go of its bytes once read, so the request — which
// lives as long as the response — does not keep them.
func TestSentBodyReleasesItsBytes(t *testing.T) {
	s := &sentBody{b: []byte("hello world")}
	got, err := io.ReadAll(s)
	if err != nil || string(got) != "hello world" {
		t.Fatalf("read %q, %v", got, err)
	}
	if s.b != nil {
		t.Error("bytes still referenced after EOF")
	}
}
