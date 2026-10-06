package store

import (
	"context"
	"testing"
	"time"
)

// turn is one request of a chat, as the context rule reads it.
type turn struct {
	model  string
	prompt int
	status int
	path   string
}

func recordTurns(t *testing.T, st *Store, chat string, start time.Time, turns []turn) {
	t.Helper()
	for i, tr := range turns {
		status, path := tr.status, tr.path
		if status == 0 {
			status = 200
		}
		if path == "" {
			path = "/v1/messages"
		}
		// Cache reads carry most of a cached prompt; the rule adds all three.
		if err := st.RecordUsage(context.Background(), UsageEvent{
			At: start.Add(time.Duration(i) * time.Minute), ConversationID: chat,
			Model: tr.model, Path: path, Status: status,
			InputTokens: 10, CacheReadTokens: tr.prompt - 110, CacheWriteTokens: 100,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// A chat's context is its main model's, and it compacts when that falls by
// more than half. The small model's titling and the gateway's own requests
// say nothing about it, and neither does a failed turn.
func TestChatContextFollowsTheMainModel(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	start := time.Now().Add(-time.Hour)

	recordTurns(t, st, "c1", start, []turn{
		{model: "small", prompt: 2_000}, // the client titling the session
		{model: "big", prompt: 100_000},
		{model: "big", prompt: 300_000},
		{model: "small", prompt: 1_500},
		{model: "big", prompt: 600_000},
		{model: "big", prompt: 900_000, status: 400},        // refused: not a turn
		{model: "big", prompt: 700_000, path: InternalPath}, // the gateway's own
		{model: "big", prompt: 40_000},                      // compacted
		{model: "big", prompt: 200_000},
		{model: "big", prompt: 30_000}, // compacted again
		{model: "big", prompt: 20_000}, // below the floor: ordinary
	})

	got, err := st.ChatContexts(ctx, []string{"c1", "never-seen"}, start.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	c := got["c1"]
	if c.Model != "big" || c.Now != 20_000 || c.Peak != 600_000 || c.Compactions != 2 {
		t.Errorf("context = %+v; want big, now 20k, peak 600k, 2 compactions", c)
	}
	if c.CompactedAt != nil {
		t.Error("the list carries compaction times it does not show")
	}
	if _, ok := got["never-seen"]; ok {
		t.Error("a chat with no requests got a context")
	}

	// The detail reads the same requests the same way, and says when.
	events, err := st.ChatEvents(ctx, "c1", 0)
	if err != nil {
		t.Fatal(err)
	}
	d := ContextOf(events)
	if d.Now != c.Now || d.Peak != c.Peak || d.Compactions != c.Compactions || len(d.CompactedAt) != 2 {
		t.Errorf("detail = %+v, list = %+v; they must agree", d, c)
	}
	if !d.CompactedAt[0].Before(d.CompactedAt[1]) {
		t.Errorf("compaction times out of order: %v", d.CompactedAt)
	}

	// And the list carries it.
	report, err := st.Chats(ctx, start.Add(-time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Chats) != 1 || report.Chats[0].Context.Peak != 600_000 {
		t.Errorf("chat list context = %+v", report.Chats)
	}
}

// The window is the list's: a chat whose big turns fell before it is measured
// on what is left.
func TestChatContextKeepsToTheWindow(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	start := time.Now().Add(-time.Hour)
	recordTurns(t, st, "c", start, []turn{
		{model: "big", prompt: 800_000},
		{model: "big", prompt: 100_000},
		{model: "big", prompt: 120_000},
	})
	got, err := st.ChatContexts(ctx, []string{"c"}, start.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if c := got["c"]; c.Peak != 120_000 || c.Compactions != 0 {
		t.Errorf("context = %+v; want only the turns inside the window", c)
	}
	if got, _ := st.ChatContexts(ctx, nil, start); len(got) != 0 {
		t.Error("no chats asked for, some answered")
	}
}

// A long chat is read where it is now: the detail returns its latest requests,
// still oldest first, rather than its first few hundred.
func TestChatEventsAreTheLatest(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	turns := make([]turn, 8)
	for i := range turns {
		turns[i] = turn{model: "big", prompt: 1_000 * (i + 1)}
	}
	recordTurns(t, st, "c", time.Now().Add(-time.Hour), turns)

	events, err := st.ChatEvents(ctx, "c", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("%d events", len(events))
	}
	for i, want := range []int64{6_000, 7_000, 8_000} {
		if got := events[i].PromptTokens(); got != want {
			t.Errorf("event %d prompt = %d, want %d: the latest three, oldest first", i, got, want)
		}
	}
}
