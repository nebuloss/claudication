package passes

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"claudication/internal/request"
)

// The splice paths decline any body whose top-level keys are duplicated or
// spelled with escapes, and hand it to a whole-envelope path that decodes for
// itself. Those bodies are rare, which is exactly why the slow paths need
// their own tests: nothing in ordinary traffic would notice them breaking,
// and a request that reaches them is one the upstream would otherwise refuse.

// A system key spelled with an escape is still the system key to every JSON
// reader, the upstream's included, so the environment line in it must still
// be reworded.
func TestNormaliseSystemThroughTheEnvelopeWhenTheKeyIsEscaped(t *testing.T) {
	body := []byte(`{"model":"m","\u0073ystem":"Is directory a git repo: yes","messages":[]}`)
	out := NormaliseSystem(body)
	blocks, ok := systemOf(t, out)
	if !ok || len(blocks) != 1 || blocks[0] != "Git repository: yes" {
		t.Errorf("system = %q (present %v), want the line reworded", blocks, ok)
	}
}

// encoding/json keeps the last of two keys, and so does the upstream; the
// slow path must rewrite that one, not the first.
func TestNormaliseSystemThroughTheEnvelopeWhenTheKeyIsDuplicated(t *testing.T) {
	body := []byte(`{"system":"first","system":[{"type":"text","text":""},{"type":"text","text":"Is directory a git repo: no"}]}`)
	out := NormaliseSystem(body)
	blocks, ok := systemOf(t, out)
	if !ok || !reflect.DeepEqual(blocks, []string{"Git repository: no"}) {
		t.Errorf("system = %q, want the last value with its empty block dropped and line reworded", blocks)
	}

	// Every block empty: the key goes, which the upstream accepts, rather
	// than an empty array it promises nothing about.
	body = []byte(`{"system":"x","system":[{"type":"text","text":""}],"model":"m"}`)
	out = NormaliseSystem(body)
	if _, ok := systemOf(t, out); ok {
		t.Errorf("system still present: %s", out)
	}
	var env map[string]string
	if err := json.Unmarshal(out, &env); err != nil || env["model"] != "m" {
		t.Errorf("the rest of the envelope was lost: %s", out)
	}
}

// When the slow path finds nothing to change it must return the caller's own
// bytes: the pipeline decides whether a pass acted by slice identity.
func TestNormaliseSystemEnvelopeReturnsTheSameBytesWhenItChangesNothing(t *testing.T) {
	for _, body := range []string{
		// Escaped key, no system at all; the marker is in a message.
		`{"\u006dodel":"m","messages":[{"role":"user","content":[{"type":"text","text":""}]}]}`,
		// Duplicated system with nothing in it to change.
		`{"system":"a","system":"b","messages":[{"content":[{"type":"text","text":""}]}]}`,
	} {
		b := []byte(body)
		if out := NormaliseSystem(b); &out[0] != &b[0] {
			t.Errorf("rewrote %s into %s", body, out)
		}
	}
	// A caller that wrongly claims the body is valid gets its body back, not
	// a half-built one.
	b := []byte(`{"\u0073ystem":"Is directory a git repo: x",`)
	if out := normaliseSystem(b, true); &out[0] != &b[0] {
		t.Errorf("rewrote an unparseable body: %s", out)
	}
}

// The same for messages: an escaped or repeated messages key goes the slow
// way, and the empty block must still be dropped from the value the upstream
// will read.
func TestDropEmptyMessageTextThroughTheEnvelope(t *testing.T) {
	cases := []string{
		`{"\u006dessages":[{"role":"user","content":[{"type":"text","text":""},{"type":"text","text":"hi"}]}]}`,
		`{"messages":[],"messages":[{"role":"user","content":[{"type":"text","text":""},{"type":"text","text":"hi"}]}]}`,
	}
	for _, body := range cases {
		out := DropEmptyMessageText([]byte(body))
		got := contentOf(t, out)
		if !reflect.DeepEqual(got, [][]string{{"text:hi"}}) {
			t.Errorf("DropEmptyMessageText(%s) content = %v", body, got)
		}
	}

	// Nothing the slow path can or should change: the same bytes come back.
	for _, body := range []string{
		`{"\u006dodel":"m","system":[{"type":"text","text":""}]}`,                       // no messages
		`{"messages":1,"messages":"x","text":""}`,                                       // not an array
		`{"messages":[],"messages":[{"content":[{"type":"text","text":""}]}]}`,          // all empty: kept
		`{"messages":[],"messages":[{"content":"bare","x":{"type":"text","text":""}}]}`, // bare string
		`{"messages":"not an array","x":{"text":""}}`,                                   // fast path, not an array
	} {
		b := []byte(body)
		if out := DropEmptyMessageText(b); &out[0] != &b[0] {
			t.Errorf("rewrote %s into %s", body, out)
		}
	}
	b := []byte(`{"\u006dessages":[{"text":""}`)
	if out := dropEmptyMessageText(b, true); &out[0] != &b[0] {
		t.Errorf("rewrote an unparseable body: %s", out)
	}
}

// A body the splice will not touch still needs the attribution block, or opus
// and sonnet refuse it with a 429 that looks like quota exhaustion.
func TestEnsureAttributionThroughTheEnvelope(t *testing.T) {
	body := []byte(`{"\u0073ystem":"be brief","model":"m"}`)
	p := request.Peek(body)
	if !p.Valid || string(p.System) != `"be brief"` {
		t.Fatalf("prologue = %+v", p)
	}
	out := EnsureAttribution(body, p)
	blocks, ok := systemOf(t, out)
	if !ok || !reflect.DeepEqual(blocks, []string{ClaudeCodeAttribution, "be brief"}) {
		t.Errorf("system = %q, want attribution first and the client's text kept", blocks)
	}

	// Not known to be valid: the splice is not attempted, the envelope is.
	body = []byte(`{"system":[{"type":"text","text":"x"}],"model":"m"}`)
	out = EnsureAttribution(body, request.Prologue{System: json.RawMessage(`[{"type":"text","text":"x"}]`)})
	blocks, _ = systemOf(t, out)
	if !reflect.DeepEqual(blocks, []string{ClaudeCodeAttribution, "x"}) {
		t.Errorf("system = %q", blocks)
	}

	// A system field of neither shape is the upstream's to reject.
	body = []byte(`{"system":5}`)
	if out := EnsureAttribution(body, request.Peek(body)); &out[0] != &body[0] {
		t.Errorf("rewrote a system of neither shape: %s", out)
	}
	// Every accepted string counts, not just the CLI's own.
	body = []byte(`{"system":"You are a Claude agent, built on Anthropic's Claude Agent SDK."}`)
	if out := EnsureAttribution(body, request.Peek(body)); &out[0] != &body[0] {
		t.Errorf("rewrote an already attributed body: %s", out)
	}
}

// A restorer with nothing to restore would rewrite every response line for
// nothing; nil is how the relay knows to copy bytes straight through.
func TestNewNameRestorer(t *testing.T) {
	if NewNameRestorer(nil) != nil || NewNameRestorer(map[string]string{}) != nil {
		t.Error("a restorer was built with no names to restore")
	}
	n := NewNameRestorer(map[string]string{"todowrite_": "todowrite"})
	got := string(n.Translate([]byte(`{"name":"todowrite_"}` + "\n")))
	if got != `{"name":"todowrite"}`+"\n" {
		t.Errorf("Translate = %q", got)
	}
}

// Past calls in the transcript carry the refused name too, and the upstream
// checks those; tool results and blocks that are not calls are left as sent.
func TestRewriteRefusedToolNamesInTheTranscript(t *testing.T) {
	body := []byte(`{"tools":[{"name":"todowrite"}],"messages":[` +
		`{"role":"user","content":"todowrite"},` +
		`{"role":"assistant","content":[{"type":"text","text":"\"todowrite\""},{"type":"tool_use","id":"t1","name":"todowrite","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"todowrite"}]},` +
		`{"no_content":"todowrite"}` +
		`]}`)
	out, names := RewriteRefusedToolNames(body)
	if names["todowrite_"] != "todowrite" {
		t.Fatalf("names = %v", names)
	}
	var env struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env.Messages[1].Content), `"name":"todowrite_"`) ||
		!strings.Contains(string(env.Messages[1].Content), `"text":"\"todowrite\""`) {
		t.Errorf("assistant turn = %s, want the call renamed and the text untouched", env.Messages[1].Content)
	}
	if string(env.Messages[0].Content) != `"todowrite"` {
		t.Errorf("a string content was changed: %s", env.Messages[0].Content)
	}
	if !strings.Contains(string(env.Messages[2].Content), `"content":"todowrite"`) {
		t.Errorf("a tool result was changed: %s", env.Messages[2].Content)
	}
}

// The surrogate pass must refresh the prologue, or the attribution pass after
// it rebuilds the system array from the bytes just repaired and puts the
// broken escape straight back.
func TestDefaultPipelineRepairsSurrogatesBeforeAttribution(t *testing.T) {
	body := []byte(`{"system":"cut \ud83d mid-emoji","messages":[]}`)
	st := State{Prologue: request.Peek(body)}
	out, applied := Default(Options{Attribution: true}).Run(body, &st, nil)
	if !reflect.DeepEqual(applied, []string{"surrogates", "attribution"}) {
		t.Errorf("applied = %v", applied)
	}
	if bytes.Contains(out, []byte(`\ud83d`)) {
		t.Errorf("the lone surrogate survived: %s", out)
	}
	if !bytes.Contains(st.Prologue.System, []byte(`\ufffd`)) {
		t.Errorf("prologue system = %s, want the repaired bytes", st.Prologue.System)
	}
	blocks, _ := systemOf(t, out)
	if len(blocks) != 2 || blocks[0] != ClaudeCodeAttribution || blocks[1] != "cut \ufffd mid-emoji" {
		t.Errorf("system = %q", blocks)
	}
}

// Image fitting is the one pass an operator switches on, so it must do
// nothing while off and act once on — including on an image so thin that
// scaling would round one side to zero pixels.
func TestDefaultPipelineImagesFollowTheSwitch(t *testing.T) {
	body := bodyWith(t, ManyImages+1, MaxEdge*2+1, 1)
	on := false
	pl := Default(Options{FitImages: func() bool { return on }, Log: slog.New(slog.DiscardHandler)})

	if _, applied := pl.Run(body, &State{Prologue: request.Peek(body)}, nil); len(applied) != 0 {
		t.Errorf("applied = %v with fitting off", applied)
	}
	on = true
	out, applied := pl.Run(body, &State{Prologue: request.Peek(body)}, nil)
	if !reflect.DeepEqual(applied, []string{"images"}) {
		t.Fatalf("applied = %v, want [images]", applied)
	}
	if d := dimensionsIn(t, out)[0]; d.Width != MaxEdge || d.Height != 1 {
		t.Errorf("first image = %dx%d, want %dx1: never zero pixels high", d.Width, d.Height, MaxEdge)
	}

	// Few images: nothing to do even with the switch on.
	few := bodyWith(t, 2, 16, 16)
	if _, applied := pl.Run(few, &State{Prologue: request.Peek(few)}, nil); len(applied) != 0 {
		t.Errorf("applied = %v on a two-image request", applied)
	}
}
