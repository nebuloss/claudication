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

// Every model field becomes the virtual name, however the answer is cut into
// writes — including a cut inside the key or inside the value — and nothing
// else changes. A model key in text is escaped and left alone.
func TestModelRenamerAcrossWrites(t *testing.T) {
	answer := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-opus-5-5-20260101\",\"id\":\"m\"}}\n\n" +
		"event: content_block_delta\ndata: {\"delta\":{\"text\":\"say \\\"model\\\":\\\"x\\\"\"}}\n\n" +
		"data: {\"response\":{\"model\":\"claude-opus-5-5\"}}\n\n"
	want := strings.ReplaceAll(answer, `"model":"claude-opus-5-5-20260101"`, `"model":"claude-smart"`)
	want = strings.ReplaceAll(want, `"model":"claude-opus-5-5"`, `"model":"claude-smart"`)

	for size := 1; size <= len(answer); size++ {
		rec := httptest.NewRecorder()
		m := NewModelRenamer(rec, "claude-smart")
		for i := 0; i < len(answer); i += size {
			end := min(i+size, len(answer))
			if n, err := m.Write([]byte(answer[i:end])); err != nil || n != end-i {
				t.Fatalf("write: %d %v", n, err)
			}
		}
		m.Finish()
		if got := rec.Body.String(); got != want {
			t.Fatalf("writes of %d bytes:\ngot  %q\nwant %q", size, got, want)
		}
	}
}

// Nothing that could not be a model field is held back: an event goes out
// with the write that carried it, so streaming is not delayed.
func TestModelRenamerHoldsNothingBack(t *testing.T) {
	rec := httptest.NewRecorder()
	m := NewModelRenamer(rec, "claude-smart")
	event := "data: {\"delta\":{\"text\":\"hi\"}}\n\n"
	_, _ = m.Write([]byte(event))
	if rec.Body.String() != event {
		t.Errorf("held back: %q", rec.Body.String())
	}
}
