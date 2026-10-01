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

// Keys match exactly, as they do for the span functions and the upstream.
// encoding/json folds case, and a prologue that read "SYSTEM" as system sent
// the passes looking for a span that is not there.
func TestPeekMatchesKeysExactly(t *testing.T) {
	p := Peek([]byte(`{"Model":"wrong","SYSTEM":"s","Stream":true,"model":"m"}`))
	if !p.Valid || p.Model != "m" || p.Stream || p.System != nil {
		t.Errorf("Peek = %+v, want only the exactly spelled model", p)
	}

	// An escape still spells the key, and the last of two wins.
	escaped := `"mod` + "\\" + `u0065l"`
	p = Peek([]byte(`{` + escaped + `:"a","stream":true,"model":"b"}`))
	if !p.Valid || p.Model != "b" || !p.Stream {
		t.Errorf("Peek = %+v, want the escaped key read and the last model kept", p)
	}
	p = Peek([]byte(`{` + escaped + `:"a"}`))
	if p.Model != "a" {
		t.Errorf("Model = %q, want the escaped key decoded", p.Model)
	}
}
