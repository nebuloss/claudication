package upstream

import (
	"encoding/json"
	"testing"
)

// The case that matters most: Claude Code's own traffic must come out the
// other side byte for byte. It already leads with an accepted block, so there
// is nothing to add and nothing to re-encode.
func TestAttributionLeavesAnAttributedBodyAlone(t *testing.T) {
	for name, body := range map[string]string{
		"cli": `{"model":"claude-opus-5","system":[{"type":"text","text":"` +
			ClaudeCodeAttribution + `"},{"type":"text","text":"more"}],"messages":[]}`,
		"agent sdk": `{"system":[{"type":"text","text":` +
			`"You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK."}]}`,
		"agent": `{"system":[{"type":"text","text":` +
			`"You are a Claude agent, built on Anthropic's Claude Agent SDK."}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			got := EnsureAttribution([]byte(body), Peek([]byte(body)))
			if string(got) != body {
				t.Errorf("body was rewritten:\n got %s\nwant %s", got, body)
			}
		})
	}
}

func TestAttributionIsAddedWhenAbsent(t *testing.T) {
	cases := map[string]string{
		"no system at all": `{"model":"claude-opus-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
		"a system string":  `{"model":"claude-opus-5","system":"You are helpful."}`,
		"other blocks":     `{"model":"claude-opus-5","system":[{"type":"text","text":"You are helpful."}]}`,
		// Position is what the backend checks: the same string second over is
		// refused, so it has to be displaced rather than left where it is.
		"attribution second": `{"system":[{"type":"text","text":"You are helpful."},` +
			`{"type":"text","text":"` + ClaudeCodeAttribution + `"}]}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			var out struct {
				System []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"system"`
			}
			if err := json.Unmarshal(EnsureAttribution([]byte(body), Peek([]byte(body))), &out); err != nil {
				t.Fatalf("result is not valid JSON: %v", err)
			}
			if len(out.System) == 0 {
				t.Fatal("no system blocks")
			}
			if out.System[0].Text != ClaudeCodeAttribution {
				t.Errorf("first block = %q, want the attribution", out.System[0].Text)
			}
			if out.System[0].Type != "text" {
				t.Errorf("first block type = %q, want text", out.System[0].Type)
			}
		})
	}
}

// Whatever the caller sent has to survive alongside it, in order.
func TestAttributionKeepsTheRestOfTheRequest(t *testing.T) {
	body := `{"model":"claude-opus-5","max_tokens":64,"temperature":0.5,` +
		`"system":[{"type":"text","text":"first"},{"type":"text","text":"second"}],` +
		`"messages":[{"role":"user","content":"hello"}]}`

	var out struct {
		Model       string  `json:"model"`
		MaxTokens   int     `json:"max_tokens"`
		Temperature float64 `json:"temperature"`
		System      []struct {
			Text string `json:"text"`
		} `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(EnsureAttribution([]byte(body), Peek([]byte(body))), &out); err != nil {
		t.Fatal(err)
	}

	if out.Model != "claude-opus-5" || out.MaxTokens != 64 || out.Temperature != 0.5 {
		t.Errorf("parameters changed: %+v", out)
	}
	if len(out.System) != 3 ||
		out.System[1].Text != "first" || out.System[2].Text != "second" {
		t.Errorf("system order not preserved: %+v", out.System)
	}
	if len(out.Messages) != 1 || out.Messages[0].Content != "hello" {
		t.Errorf("messages changed: %+v", out.Messages)
	}
}

// A body we cannot read is the client's to get wrong and the upstream's to
// reject. Guessing at it would turn a clear 400 into something stranger.
func TestAttributionLeavesUnparseableBodiesAlone(t *testing.T) {
	for _, body := range []string{"", "not json", `{"system":`, `{"system":42}`, `[]`} {
		if got := EnsureAttribution([]byte(body), Peek([]byte(body))); string(got) != body {
			t.Errorf("body %q was rewritten to %q", body, got)
		}
	}
}
