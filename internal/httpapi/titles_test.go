package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
)

// The cached prefix must survive untouched.
//
// This is the whole cost argument, and it was measured rather than assumed: a
// side call that keeps the caller's system prompt and tools reads the
// conversation's cache in full (11406 tokens), while one that swaps in its own
// system prompt reads nothing and writes a second 7886-token entry on top. If
// a later change starts rewriting the prefix here, the feature silently
// becomes many times more expensive and nothing else would catch it.
func TestTitleBodyLeavesTheCachedPrefixAlone(t *testing.T) {
	const original = `{
	  "model": "claude-sonnet-5",
	  "max_tokens": 8192,
	  "stream": true,
	  "thinking": {"type": "enabled", "budget_tokens": 4000},
	  "tool_choice": {"type": "any"},
	  "system": [{"type": "text", "text": "You are Claude Code.",
	              "cache_control": {"type": "ephemeral"}}],
	  "tools": [{"name": "Read", "description": "read a file",
	             "input_schema": {"type": "object"},
	             "cache_control": {"type": "ephemeral"}}],
	  "messages": [{"role": "user", "content": "fix the parser"}]
	}`

	out, ok := titleBody([]byte(original))
	if !ok {
		t.Fatal("titleBody refused a well-formed request")
	}

	var before, after map[string]json.RawMessage
	if err := json.Unmarshal([]byte(original), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &after); err != nil {
		t.Fatalf("the result is not JSON: %v", err)
	}

	// Byte-identical, cache_control markers included. Anything else changes the
	// prefix and forfeits the cache.
	for _, field := range []string{"system", "tools", "model"} {
		if string(before[field]) != string(after[field]) {
			t.Errorf("%s was rewritten:\n  before %s\n  after  %s",
				field, before[field], after[field])
		}
	}

	// Streaming would mean parsing a stream for one line; thinking would spend
	// the whole output budget before reaching the answer; a forced tool_choice
	// would make a tool call the only legal reply.
	for _, field := range []string{"stream", "thinking", "tool_choice"} {
		if _, present := after[field]; present {
			t.Errorf("%s survived into the title request", field)
		}
	}

	// Tools stay. They are part of the prefix, and dropping them truncates it
	// before the messages, which is most of what there is to read back.
	if _, present := after["tools"]; !present {
		t.Error("tools were dropped, which truncates the cached prefix")
	}

	var messages []json.RawMessage
	if err := json.Unmarshal(after["messages"], &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("got %d messages, want the original plus one", len(messages))
	}
	if !strings.Contains(string(messages[0]), "fix the parser") {
		t.Error("the original message was not kept first; the prefix must not move")
	}
	var appended struct{ Role, Content string }
	if err := json.Unmarshal(messages[1], &appended); err != nil {
		t.Fatal(err)
	}
	if appended.Role != "user" {
		t.Errorf("appended role = %q, want user", appended.Role)
	}
	if !strings.Contains(appended.Content, "title") {
		t.Errorf("the appended message does not ask for a title: %q", appended.Content)
	}

	var budget int
	if err := json.Unmarshal(after["max_tokens"], &budget); err != nil {
		t.Fatal(err)
	}
	if budget > titleMaxTokens {
		t.Errorf("max_tokens = %d, want no more than %d", budget, titleMaxTokens)
	}
}

func TestTitleBodyRefusesWhatItCannotUse(t *testing.T) {
	for _, body := range []string{
		``,
		`not json`,
		`{"model":"claude-sonnet-5"}`,        // no messages
		`{"model":"m","messages":[]}`,        // nothing to title
		`{"model":"m","messages":"wrong"}`,   // messages is not a list
	} {
		if _, ok := titleBody([]byte(body)); ok {
			t.Errorf("titleBody(%.30q) accepted a request it cannot use", body)
		}
	}
}

func TestTitleFromAnswer(t *testing.T) {
	for _, c := range []struct{ name, body, want string }{{
		name: "a plain answer",
		body: `{"content":[{"type":"text","text":"Fix the SSE parser"}]}`,
		want: "Fix the SSE parser",
	}, {
		name: "quoted and full-stopped despite being told not to",
		body: `{"content":[{"type":"text","text":"\"Fix the SSE parser.\""}]}`,
		want: "Fix the SSE parser",
	}, {
		name: "a preamble the model added anyway",
		body: `{"content":[{"type":"text","text":"Title: Fix the SSE parser"}]}`,
		want: "Fix the SSE parser",
	}, {
		name: "only the first line",
		body: `{"content":[{"type":"text","text":"Fix the parser\n\nLet me know if…"}]}`,
		want: "Fix the parser",
	}, {
		// The model ignored the instruction and answered the conversation. A
		// sentence is not a title, and a wrong name is worse than none.
		name: "an answer rather than a title",
		body: `{"content":[{"type":"text","text":"I'll start by reading the parser file to understand how it currently handles the event stream, and then I will propose a fix."}]}`,
		want: "",
	}, {
		// With tools in the request a model may call one instead of replying.
		// There is no text in that, and no title.
		name: "a tool call instead of a reply",
		body: `{"content":[{"type":"tool_use","id":"t1","name":"Read"}]}`,
		want: "",
	}, {
		name: "nothing at all",
		body: `{"content":[]}`,
		want: "",
	}, {
		name: "not an answer",
		body: `garbage`,
		want: "",
	}} {
		if got := titleFromAnswer([]byte(c.body)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
