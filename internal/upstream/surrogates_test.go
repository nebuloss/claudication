package upstream

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFixLoneSurrogatesRepairsAnUnpairedEscape(t *testing.T) {
	// What a tool produces when its output is cut mid-emoji.
	body := []byte(`{"content":"output \ud800 truncated"}`)

	out, n := FixLoneSurrogates(body)
	if n != 1 {
		t.Fatalf("replaced %d, want 1", n)
	}
	if strings.Contains(string(out), `\ud800`) {
		t.Errorf("the lone surrogate survived: %s", out)
	}
	if !strings.Contains(string(out), `\ufffd`) {
		t.Errorf("no replacement written: %s", out)
	}
	// The repair has to leave something the upstream will actually accept.
	var parsed struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("repaired body does not parse: %v", err)
	}
	if !strings.HasPrefix(parsed.Content, "output ") ||
		!strings.HasSuffix(parsed.Content, " truncated") {
		t.Errorf("surrounding text changed: %q", parsed.Content)
	}
}

func TestFixLoneSurrogatesLeavesWellFormedPairsAlone(t *testing.T) {
	// A real emoji is two escapes that belong together; breaking it would
	// corrupt text that was fine.
	body := []byte(`{"content":"hello 😀 world"}`)
	out, n := FixLoneSurrogates(body)
	if n != 0 {
		t.Errorf("replaced %d in a well-formed pair", n)
	}
	if string(out) != string(body) {
		t.Errorf("body changed:\n got %s\nwant %s", out, body)
	}
}

func TestFixLoneSurrogatesHandlesEveryShape(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"lone high", `{"a":"x \ud800 y"}`, 1},
		{"lone low", `{"a":"x \udc00 y"}`, 1},
		{"high at end of string", `{"a":"x \ud800"}`, 1},
		{"two lone highs", `{"a":"\ud800\ud801"}`, 2},
		{"uppercase hex", `{"a":"x \uD800 y"}`, 1},
		{"pair then lone", `{"a":"😀 \ud800"}`, 1},
		{"lone then pair", `{"a":"\ud800 😀"}`, 1},
		{"non-surrogate escape", `{"a":"tab	here"}`, 0},
		{"no escapes at all", `{"a":"plain text"}`, 0},
		{"escaped backslash, not an escape", `{"a":"literal \\ud800 text"}`, 0},
	} {
		out, n := FixLoneSurrogates([]byte(tc.body))
		if n != tc.want {
			t.Errorf("%s: replaced %d, want %d (got %s)", tc.name, n, tc.want, out)
			continue
		}
		if tc.want > 0 {
			if err := json.Unmarshal(out, &map[string]any{}); err != nil {
				t.Errorf("%s: repaired body does not parse: %v", tc.name, err)
			}
		}
	}
}

func TestFixLoneSurrogatesReturnsTheSameBytesWhenItChangesNothing(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hello"}]}`)
	out, n := FixLoneSurrogates(body)
	if n != 0 {
		t.Fatalf("replaced %d", n)
	}
	if &out[0] != &body[0] {
		t.Error("body was copied when nothing needed repairing")
	}
}

func TestFixLoneSurrogatesDoesNotMutateTheCaller(t *testing.T) {
	// Do() may hand the same slice to a retry; repairing must not scribble on
	// the buffer the caller still holds.
	body := []byte(`{"a":"\ud800"}`)
	before := string(body)
	if _, n := FixLoneSurrogates(body); n != 1 {
		t.Fatalf("replaced %d, want 1", n)
	}
	if string(body) != before {
		t.Errorf("the caller's slice was modified: %s", body)
	}
}
