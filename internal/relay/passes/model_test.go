package passes

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSetModelChangesOnlyTheModel(t *testing.T) {
	in := `{"model":"claude-smart","max_tokens":5,"messages":[{"role":"user","content":"\"model\":\"x\""}]}`
	out, ok := SetModel([]byte(in), "claude-opus-5-5")
	if !ok {
		t.Fatal("not replaced")
	}
	want := `{"model":"claude-opus-5-5","max_tokens":5,"messages":[{"role":"user","content":"\"model\":\"x\""}]}`
	if string(out) != want {
		t.Errorf("got  %s\nwant %s", out, want)
	}
	if _, ok := SetModel([]byte(`{"messages":[]}`), "m"); ok {
		t.Error("a body without a model was reported replaced")
	}
}

// The streamed events of both dialects that carry the model.
var modelEvents = []string{"message_start", "response.created", "response.completed"}

// writeIn sends answer through a renamer in writes of size bytes and returns
// what reached the client.
func writeIn(t *testing.T, contentType, answer string, size int) string {
	t.Helper()
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", contentType)
	m := NewModelRenamer(rec, "claude-smart", modelEvents)
	for i := 0; i < len(answer); i += size {
		end := min(i+size, len(answer))
		if n, err := m.Write([]byte(answer[i:end])); err != nil || n != end-i {
			t.Fatalf("write: %d %v", n, err)
		}
	}
	m.Finish()
	return rec.Body.String()
}

// The model field of each event that carries one becomes the virtual name,
// however the answer is cut into writes — including a cut inside the event
// name, the key or the value — and nothing else changes: not a model key in
// text, which is escaped, and not one in an event that does not name the
// model, such as a tool call opening with its input.
func TestModelRenamerAcrossWrites(t *testing.T) {
	answer := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-opus-5-5-20260101\",\"id\":\"m\",\"content\":[]}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"input\":{\"model\":\"gpt-4o\"}}}\n\n" +
		"event: content_block_delta\ndata: {\"delta\":{\"text\":\"say \\\"model\\\":\\\"x\\\"\"}}\n\n" +
		"event: response.completed\ndata: {\"response\":{\"model\":\"claude-opus-5-5\",\"output\":[{\"model\":\"kept\"}]}}\n\n"
	want := strings.Replace(answer, `"model":"claude-opus-5-5-20260101"`, `"model":"claude-smart"`, 1)
	want = strings.Replace(want, `"model":"claude-opus-5-5"`, `"model":"claude-smart"`, 1)

	for size := 1; size <= len(answer); size++ {
		if got := writeIn(t, "text/event-stream", answer, size); got != want {
			t.Fatalf("writes of %d bytes:\ngot  %q\nwant %q", size, got, want)
		}
	}
}

// A non-streaming answer has its top-level model renamed and nothing else. A
// tool call's input is a raw JSON object here, not a string, so a model
// argument in it is an unescaped "model" key — and it is the caller's.
func TestModelRenamerLeavesToolInputAlone(t *testing.T) {
	answer := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5",` +
		`"content":[{"type":"tool_use","id":"toolu_1","name":"spawn","input":{"model":"claude-haiku-4-5","task":"x"}}],` +
		`"stop_reason":"tool_use","usage":{"input_tokens":3,"output_tokens":4}}`
	want := strings.Replace(answer, `"model":"claude-opus-5-5"`, `"model":"claude-smart"`, 1)

	for _, size := range []int{1, 7, len(answer)} {
		if got := writeIn(t, "application/json", answer, size); got != want {
			t.Fatalf("writes of %d bytes:\ngot  %s\nwant %s", size, got, want)
		}
	}

	// A refusal names no model and goes as it came.
	refusal := `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`
	if got := writeIn(t, "application/json", refusal, len(refusal)); got != refusal {
		t.Errorf("refusal changed: %s", got)
	}
}

// The body changes length, so a Content-Length the upstream sent would
// describe one that is no longer being sent.
func TestModelRenamerDropsContentLength(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "application/json")
	rec.Header().Set("Content-Length", "42")
	m := NewModelRenamer(rec, "claude-smart", modelEvents)
	m.WriteHeader(200)
	_, _ = m.Write([]byte(`{"model":"claude-opus-5-5"}`))
	m.Finish()
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Errorf("Content-Length = %q, want none", got)
	}
}

// Nothing that could not be a model field is held back: an event goes out
// with the write that carried it, so streaming is not delayed.
func TestModelRenamerHoldsNothingBack(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "text/event-stream")
	m := NewModelRenamer(rec, "claude-smart", modelEvents)
	for event, want := range map[string]string{
		"event: content_block_delta\ndata: {\"delta\":{\"text\":\"hi\"}}\n\n": "",
		"event: message_start\ndata: {\"message\":{\"model\":\"m\"}}\n\n": "event: message_start\ndata: {\"message\":{\"model\":\"claude-smart\"}}\n\n",
	} {
		if want == "" {
			want = event
		}
		before := rec.Body.Len()
		_, _ = m.Write([]byte(event))
		if got := rec.Body.String()[before:]; got != want {
			t.Errorf("held back: %q, want %q", got, want)
		}
	}
}
