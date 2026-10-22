package passes

import "testing"

func TestFitMaxTokens(t *testing.T) {
	cases := []struct {
		name, in, want string
		limit          int
		changed        bool
	}{
		{"over the cap is lowered to it",
			`{"model":"large","max_tokens":128000,"messages":[]}`,
			`{"model":"large","max_tokens":64000,"messages":[]}`, 64000, true},
		{"at the cap is left alone",
			`{"model":"large","max_tokens":64000,"messages":[]}`, "", 64000, false},
		{"under the cap is left alone",
			`{"model":"large","max_tokens":100,"messages":[]}`, "", 64000, false},
		{"an unknown cap passes through",
			`{"model":"large","max_tokens":128000,"messages":[]}`, "", 0, false},
		{"no budget at all",
			`{"model":"large","messages":[]}`, "", 64000, false},
		{"a budget that is no plain integer",
			`{"model":"large","max_tokens":1.28e5,"messages":[]}`, "", 64000, false},
		{"a thinking budget at or over the new cap goes just under it",
			`{"max_tokens":128000,"thinking":{"type":"enabled","budget_tokens":100000},"messages":[]}`,
			`{"max_tokens":64000,"thinking":{"budget_tokens":63999,"type":"enabled"},"messages":[]}`, 64000, true},
		{"a thinking budget that fits is kept as it was written",
			`{"max_tokens":128000,"thinking":{"type":"enabled","budget_tokens":16000},"messages":[]}`,
			`{"max_tokens":64000,"thinking":{"type":"enabled","budget_tokens":16000},"messages":[]}`, 64000, true},
		{"adaptive thinking has no budget to fit",
			`{"max_tokens":128000,"thinking":{"type":"adaptive"},"messages":[]}`,
			`{"max_tokens":64000,"thinking":{"type":"adaptive"},"messages":[]}`, 64000, true},
		{"a max_tokens inside a message is not the budget",
			`{"max_tokens":10,"messages":[{"role":"user","content":"{\"max_tokens\":999999}"}]}`, "", 64000, false},
	}
	for _, c := range cases {
		out, ok := FitMaxTokens([]byte(c.in), c.limit)
		if ok != c.changed {
			t.Errorf("%s: changed = %v", c.name, ok)
			continue
		}
		want := c.want
		if !c.changed {
			want = c.in
		}
		if string(out) != want {
			t.Errorf("%s:\n got  %s\n want %s", c.name, out, want)
		}
	}
}

func TestFitMaxOutputTokens(t *testing.T) {
	in := `{"model":"large","input":[],"max_output_tokens":200000,"stream":true}`
	out, ok := FitMaxOutputTokens([]byte(in), 128000)
	if want := `{"model":"large","input":[],"max_output_tokens":128000,"stream":true}`; !ok || string(out) != want {
		t.Errorf("got %s (%v), want %s", out, ok, want)
	}
	// Codex sends none: nothing to fit, and none is added.
	none := `{"model":"large","input":[],"stream":true}`
	if out, ok := FitMaxOutputTokens([]byte(none), 128000); ok || string(out) != none {
		t.Errorf("a body without one became %s", out)
	}
	// The Messages field means nothing in a Responses body.
	other := `{"model":"large","max_tokens":200000}`
	if _, ok := FitMaxOutputTokens([]byte(other), 128000); ok {
		t.Error("max_tokens fitted on the Responses wire")
	}
}
