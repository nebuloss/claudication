package passes

import (
	"bytes"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"claudication/internal/request"
)

// The pipeline reports which passes changed the body, in order, and nothing
// for a pass that returned what it was given.
func TestPipelineReportsWhatActed(t *testing.T) {
	upper := Pass{Name: "upper", Apply: func(b []byte, _ *State) []byte { return bytes.ToUpper(b) }}
	same := Pass{Name: "same", Apply: func(b []byte, _ *State) []byte { return b }}
	off := Pass{Name: "off", Enabled: func() bool { return false },
		Apply: func([]byte, *State) []byte { t.Fatal("a disabled pass ran"); return nil }}

	out, applied := Pipeline{same, upper, off}.Run([]byte("abc"), &State{}, nil)
	if string(out) != "ABC" {
		t.Errorf("out = %q", out)
	}
	if !reflect.DeepEqual(applied, []string{"upper"}) {
		t.Errorf("applied = %v, want [upper]", applied)
	}
}

// What a crush request goes through: no attribution, the environment line in
// its system prompt, an empty block, a refused tool name. Every pass acts, in
// the documented order, and the names to restore come back for the response.
func TestDefaultPipelineOnACrushRequest(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","system":"Is directory a git repo: yes",` +
		`"tools":[{"name":"mcp_github_search","input_schema":{}}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":""},{"type":"text","text":"hi"}]}]}`)
	st := State{Prologue: request.Peek(body)}
	out, applied := Default(Options{Attribution: true, Log: slog.New(slog.DiscardHandler)}).Run(body, &st, nil)

	want := []string{"attribution", "system-text", "empty-text", "tool-names"}
	if !reflect.DeepEqual(applied, want) {
		t.Errorf("applied = %v, want %v", applied, want)
	}
	if !request.Peek(out).Valid {
		t.Fatalf("result does not parse: %s", out)
	}
	for _, s := range []string{ClaudeCodeAttribution, "Git repository: yes", `"mcp__github_search"`} {
		if !strings.Contains(string(out), s) {
			t.Errorf("missing %q in %s", s, out)
		}
	}
	if st.Names["mcp__github_search"] != "mcp_github_search" {
		t.Errorf("names to restore = %v", st.Names)
	}
}

// Claude Code's own request is already attributed and carries nothing the
// passes touch: it goes up as it came, and the relay logs no rewrites.
func TestDefaultPipelineLeavesClaudeCodeAlone(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","system":[{"type":"text","text":"` + ClaudeCodeAttribution +
		`"}],"tools":[{"name":"mcp__github__search"}],"messages":[{"role":"user","content":"hi"}]}`)
	st := State{Prologue: request.Peek(body)}
	out, applied := Default(Options{Attribution: true}).Run(body, &st, nil)
	if len(applied) != 0 || &out[0] != &body[0] {
		t.Errorf("rewrote Claude Code's request: %v\n%s", applied, out)
	}
}
