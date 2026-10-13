package titles

import (
	"bytes"
	"encoding/json"
	"strconv"
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

func TestOpensWithInit(t *testing.T) {
	if !opensWithInit(convo(crushInit, assistantSay, followUp)) {
		t.Error("crush's init prompt as the first message was not recognised")
	}
	if !opensWithInit(convo(`{"role":"user","content":"Please analyze this codebase and create a CLAUDE.md file"}`)) {
		t.Error("Claude Code's /init was not recognised")
	}
	// The marker inside CLAUDE.md, carried in a reminder, is not the user
	// asking for anything.
	if opensWithInit(convo(reminderOnly)) {
		t.Error("a marker inside a <system-reminder> was taken for the user's request")
	}
	// Only the first message counts: asking for an AGENTS.md later on is work
	// done in the chat, not how it opened.
	if opensWithInit(convo(followUp, assistantSay, crushInit)) {
		t.Error("an init prompt later in the chat was taken for its opening")
	}
}

func TestRenameDue(t *testing.T) {
	captured := store.TitleRecord{Title: "Generate AGENTS.md for codebase"}
	for _, c := range []struct {
		name            string
		current         store.TitleRecord
		requests, turns int
		init            bool
		want            bool
	}{
		{"init chat, still on the init", captured, 12, 1, true, false},
		{"init chat, the user typed again", captured, 14, 2, true, true},
		{"init chat already renamed at its second turn", store.TitleRecord{Requests: 14, Turns: 2}, 30, 3, true, false},
		{"ordinary chat, short", captured, 49, 5, false, false},
		{"ordinary chat reaches the first threshold", captured, 50, 5, false, true},
		{"renamed at 50, not yet four-fold", store.TitleRecord{Requests: 50}, 199, 9, false, false},
		{"renamed at 50, four-fold", store.TitleRecord{Requests: 50}, 200, 9, false, true},
		// A title written on the chat's first turn records one request; that
		// must not make it due at four.
		{"generated on turn one", store.TitleRecord{Requests: 1, Turns: 1}, 4, 1, false, false},
	} {
		if got := renameDue(c.current, c.requests, c.turns, c.init); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// The opening message and the latest, in order, each cut to its share, with
// tool results and reminders left out and a marker where the middle went.
func TestExcerptKeepsTheOpeningAndTheLatest(t *testing.T) {
	texts := []string{"open the parser", "", strings.Repeat("x", 3000)}
	for i := 0; i < 20; i++ {
		texts = append(texts, "middle "+strings.Repeat("m", 1000))
	}
	texts = append(texts, "", "now fix ext4 resize")

	got := excerpt(texts)
	if !strings.HasPrefix(got, "open the parser\n---\n[…]\n---\n") {
		t.Errorf("does not open with the first message and a gap marker: %.80q", got)
	}
	if !strings.HasSuffix(got, "now fix ext4 resize") {
		t.Errorf("does not end on the latest message: …%q", got[len(got)-40:])
	}
	if len(got) > excerptBudget+excerptFirst+128 {
		t.Errorf("excerpt is %d bytes, over its budget", len(got))
	}
	if excerpt([]string{"", ""}) != "" {
		t.Error("nothing typed should be no excerpt")
	}
	if got := excerpt([]string{"a", "b"}); got != "a\n---\nb" {
		t.Errorf("short chat: %q", got)
	}
}

func TestRememberDropsTheStalestChat(t *testing.T) {
	tl := &Titler{recent: map[string]recentChat{}}
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for i := 0; i < recentLimit; i++ {
		tl.remember(string(rune('a'+i%26))+strconv.Itoa(i), recentChat{excerpt: "x", model: "m", at: t0.Add(time.Duration(i) * time.Second)})
	}
	tl.remember("new", recentChat{excerpt: "x", model: "m", at: t0.Add(time.Hour)})
	if len(tl.recent) != recentLimit {
		t.Errorf("holds %d chats, want %d", len(tl.recent), recentLimit)
	}
	if _, ok := tl.recent["a0"]; ok {
		t.Error("the stalest chat was kept")
	}
	if _, ok := tl.recent["new"]; !ok {
		t.Error("the newest chat was not remembered")
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
