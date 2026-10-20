package titles

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"claudication/internal/store"
)

// compactJSON strips insignificant whitespace so two encodings of the same
// blocks compare equal.
func compactJSON(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		t.Fatalf("compact %s: %v", raw, err)
	}
	return out.String()
}

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

	out, ok := titleBody(false, []byte(original))
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

	// Unchanged as content, cache_control markers included. Compared compacted
	// rather than byte-for-byte because re-encoding the envelope drops
	// insignificant JSON whitespace, and the upstream parses the body before it
	// hashes anything — what must not move is the blocks, not their spacing.
	for _, field := range []string{"system", "tools", "model"} {
		if compactJSON(t, before[field]) != compactJSON(t, after[field]) {
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
		`{"model":"claude-sonnet-5"}`,      // no messages
		`{"model":"m","messages":[]}`,      // nothing to title
		`{"model":"m","messages":"wrong"}`, // messages is not a list
	} {
		if _, ok := titleBody(false, []byte(body)); ok {
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
		// Claude Code asks for its title through output_config.format, a
		// json_schema of one string "title", and reads that field.
		name: "Claude Code's structured answer",
		body: `{"content":[{"type":"text","text":"{\"title\":\"auth.go login bug\"}"}]}`,
		want: "auth.go login bug",
	}, {
		name: "a structured answer pretty-printed",
		body: `{"content":[{"type":"text","text":"{\n  \"title\": \"Session cookie handling\"\n}"}]}`,
		want: "Session cookie handling",
	}, {
		name: "an object without the field",
		body: `{"content":[{"type":"text","text":"{\"name\":\"x\"}"}]}`,
		want: "",
	}, {
		name: "an object cut short",
		body: `{"content":[{"type":"text","text":"{\"title\":\"auth"}]}`,
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

// crush's init prompt as it opens a chat, then the agent loop, then the user
// typing again. Shapes as crush and Claude Code send them: crush's text as a
// block, a tool result in a user-role message, and Claude Code's CLAUDE.md
// carried in a <system-reminder> block in front of what the user typed.
const (
	crushInit    = `{"role":"user","content":[{"type":"text","text":"Analyze this codebase and create/update **AGENTS.md** to help future agents work effectively in this repository."}]}`
	assistantUse = `{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"ls","input":{}}]}`
	toolResult   = `{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"Makefile"},{"type":"text","text":"<system-reminder>todo list is empty</system-reminder>"}]}`
	assistantSay = `{"role":"assistant","content":[{"type":"text","text":"Written."}]}`
	followUp     = `{"role":"user","content":"Now fix the ext4 resize in go-diskfs"}`
	reminderOnly = `{"role":"user","content":[{"type":"text","text":"<system-reminder>\n# CLAUDE.md\nPlease analyze this codebase and create a CLAUDE.md file\n</system-reminder>"},{"type":"text","text":"why does the SSE parser drop events?"}]}`
)

func convo(m ...string) []byte {
	return []byte(`{"model":"claude-opus-5","messages":[` + strings.Join(m, ",") + `]}`)
}

func TestUserTurnsCountsOnlyWhatTheUserTyped(t *testing.T) {
	for _, c := range []struct {
		name string
		body []byte
		want int
	}{
		{"init alone", convo(crushInit), 1},
		{"init and its agent loop", convo(crushInit, assistantUse, toolResult, assistantSay), 1},
		{"then the user speaks again", convo(crushInit, assistantUse, toolResult, assistantSay, followUp), 2},
		{"a reminder is not a turn on its own", convo(`{"role":"user","content":[{"type":"text","text":"<system-reminder>x</system-reminder>"}]}`), 0},
		{"not a request", []byte(`garbage`), 0},
	} {
		if got := userTurns(c.body); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

// The user's first message and the agent's text in reply, up to the user's
// next message; tool traffic and reminders left out.
func TestOpeningExchange(t *testing.T) {
	narrate := `{"role":"assistant","content":[{"type":"text","text":"Reading the tree."},{"type":"tool_use","id":"t1","name":"ls","input":{}}]}`
	summary := `{"role":"assistant","content":[{"type":"text","text":"Wrote AGENTS.md for the ZeeOS installer."}]}`
	later := `{"role":"assistant","content":[{"type":"text","text":"Resized the ext4 image."}]}`

	got := openingExchange(convo(crushInit, narrate, toolResult, summary, followUp, later))
	for _, want := range []string{"<user>\nAnalyze this codebase", "<agent>\nReading the tree.\n\nWrote AGENTS.md for the ZeeOS installer.\n</agent>"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, not := range []string{"Makefile", "todo list", "ext4", "tool_use"} {
		if strings.Contains(got, not) {
			t.Errorf("%q leaked into the opening exchange:\n%s", not, got)
		}
	}

	// Claude Code's CLAUDE.md reminder is not what the user typed.
	if got := openingExchange(convo(reminderOnly)); got != "<user>\nwhy does the SSE parser drop events?\n</user>" {
		t.Errorf("with a reminder in front: %q", got)
	}
	if got := openingExchange(convo(toolResult)); got != "" {
		t.Errorf("nothing typed should be no exchange: %q", got)
	}

	// A long reply keeps its end, where the summary is.
	long := `{"role":"assistant","content":[{"type":"text","text":"` + strings.Repeat("a", 3*openingAgent) + ` THE END"}]}`
	got = openingExchange(convo(followUp, long))
	if !strings.HasSuffix(got, "THE END\n</agent>") || len(got) > openingAgent+openingUser+64 {
		t.Errorf("long reply: %d bytes, ends %q", len(got), got[len(got)-24:])
	}
}

func TestRenameDue(t *testing.T) {
	for _, c := range []struct {
		name    string
		current store.TitleRecord
		turns   int
		want    bool
	}{
		{"the client's own, still on the first message", store.TitleRecord{}, 1, false},
		{"the client's own, the user typed again", store.TitleRecord{}, 2, false},
		{"generated on turn one, the user typed again", store.TitleRecord{Requests: 1, Turns: 1}, 2, true},
		{"already named from the opening exchange", store.TitleRecord{Requests: 3, Turns: 2}, 9, false},
		{"typed by the operator", store.TitleRecord{Manual: true}, 9, false},
	} {
		if got := renameDue(c.current, c.turns); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRenameAttemptsAreSpacedOut(t *testing.T) {
	tl := &Titler{renamed: map[string]time.Time{}}
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	if !tl.mayRename("a", t0) {
		t.Fatal("first attempt refused")
	}
	if tl.mayRename("a", t0.Add(time.Minute)) {
		t.Error("a second attempt inside the backoff was allowed")
	}
	if !tl.mayRename("b", t0.Add(time.Minute)) {
		t.Error("another chat was held back by this one's attempt")
	}
	if !tl.mayRename("a", t0.Add(retitleBackoff)) {
		t.Error("an attempt after the backoff was refused")
	}
}
