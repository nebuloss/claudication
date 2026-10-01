package request

import "testing"

// Peek is read on every request, so it must take exactly the three fields the
// relay routes on and leave the rest of the body unread.
func TestPeekReadsTheEnvelope(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"model":"claude-opus-5",` +
		`"stream":true,"system":[{"type":"text","text":"s"}],"max_tokens":10}`)
	p := Peek(body)
	if !p.Valid {
		t.Fatal("Valid = false for a well-formed body")
	}
	if p.Model != "claude-opus-5" || !p.Stream {
		t.Errorf("model/stream = %q/%v", p.Model, p.Stream)
	}
	if string(p.System) != `[{"type":"text","text":"s"}]` {
		t.Errorf("System = %s, want the raw system value as sent", p.System)
	}
}

// An unparseable body must come back as a zero prologue with Valid false:
// Valid is the only thing standing between the span functions, which do not
// validate, and a body they would splice into wrongly.
func TestPeekRejectsWhatDoesNotParse(t *testing.T) {
	for _, body := range []string{
		``,
		`{`,
		`{"model":"a",}`,
		`{"model":"a"} trailing`,
		// Right syntax, wrong type: still not a body the gateway understands.
		`{"model":"a","stream":"yes"}`,
	} {
		p := Peek([]byte(body))
		if p.Valid {
			t.Errorf("Peek(%q).Valid = true", body)
		}
		if body == `` && (p.Model != "" || p.Stream || p.System != nil) {
			t.Errorf("Peek(%q) = %+v, want zero", body, p)
		}
	}
	if p := Peek([]byte(`{}`)); !p.Valid || p.Model != "" || p.System != nil {
		t.Errorf("Peek({}) = %+v, want valid and empty", p)
	}
}
