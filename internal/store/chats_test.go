package store

import (
	"context"
	"testing"
	"time"
)

func TestChatsRollUpPerConversation(t *testing.T) {
	st := usageStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 17, 9, 14, 0, 0, time.UTC)

	// One Claude Code chat over three turns, switching model part way, with a
	// failure in the middle.
	record(t, st, UsageEvent{At: t0, ConversationID: "04c27413", Client: "Claude Code",
		KeyName: "laptop", AccountEmail: "a@example.com", Model: "claude-opus-5",
		Status: 200, InputTokens: 100, OutputTokens: 10, CacheReadTokens: 900})
	record(t, st, UsageEvent{At: t0.Add(time.Minute), ConversationID: "04c27413",
		Client: "Claude Code", KeyName: "laptop", AccountEmail: "a@example.com",
		Model: "claude-opus-5", Status: 429, Error: "refused"})
	record(t, st, UsageEvent{At: t0.Add(2 * time.Minute), ConversationID: "04c27413",
		Client: "Claude Code", KeyName: "laptop", AccountEmail: "a@example.com",
		Model: "claude-sonnet-5", Status: 200, InputTokens: 50, OutputTokens: 5})

	// A cheaper opencode chat, so ordering by spend is observable.
	record(t, st, UsageEvent{At: t0.Add(time.Hour), ConversationID: "ses_f50990",
		Client: "opencode", KeyName: "laptop", AccountEmail: "a@example.com",
		Model: "claude-sonnet-5", Status: 200, InputTokens: 10, OutputTokens: 1})

	// Two requests that named nothing.
	record(t, st, UsageEvent{At: t0, Client: "curl", Model: "claude-sonnet-5",
		Status: 200, InputTokens: 7})
	record(t, st, UsageEvent{At: t0, Client: "curl", Model: "claude-sonnet-5",
		Status: 500, Error: "boom", InputTokens: 3})

	rep, err := st.Chats(ctx, t0.Add(-time.Hour), 50)
	if err != nil {
		t.Fatal(err)
	}

	if rep.Total != 2 {
		t.Errorf("total = %d, want 2 named conversations", rep.Total)
	}
	if len(rep.Chats) != 2 {
		t.Fatalf("got %d chats, want 2", len(rep.Chats))
	}

	// Busiest first: the question a chat list answers is what something cost.
	got := rep.Chats[0]
	if got.ID != "04c27413" {
		t.Errorf("first chat = %q, want the expensive one first", got.ID)
	}
	if got.Requests != 3 {
		t.Errorf("requests = %d, want 3 turns", got.Requests)
	}
	if got.Errors != 1 {
		t.Errorf("errors = %d, want the one refusal", got.Errors)
	}
	if got.Tokens() != 1065 {
		t.Errorf("tokens = %d, want 1065 — cache reads are billed too", got.Tokens())
	}
	if got.Client != "Claude Code" {
		t.Errorf("client = %q", got.Client)
	}
	if got.KeyName != "laptop" || got.AccountEmail != "a@example.com" {
		t.Errorf("key/account = %q/%q", got.KeyName, got.AccountEmail)
	}
	// A chat that changed model mid-way is ordinary and both must show.
	if len(got.Models) != 2 {
		t.Errorf("models = %v, want both models the chat used", got.Models)
	}
	if !got.First.Equal(t0) {
		t.Errorf("first = %v, want %v", got.First, t0)
	}
	if !got.Last.Equal(t0.Add(2 * time.Minute)) {
		t.Errorf("last = %v, want the third turn", got.Last)
	}

	// The absence is reported, never folded into a chat or dropped: a UI whose
	// token total did not match the Overview would be worse than no UI.
	if rep.Unattributed.Requests != 2 {
		t.Errorf("unattributed requests = %d, want 2", rep.Unattributed.Requests)
	}
	if rep.Unattributed.Errors != 1 {
		t.Errorf("unattributed errors = %d, want 1", rep.Unattributed.Errors)
	}
	if rep.Unattributed.Tokens() != 10 {
		t.Errorf("unattributed tokens = %d, want 10", rep.Unattributed.Tokens())
	}
	// One arbitrary client's name over a mixed bag would read as a fact about
	// all of them.
	if rep.Unattributed.Client != "" {
		t.Errorf("unattributed client = %q, want empty", rep.Unattributed.Client)
	}
	for _, c := range rep.Chats {
		if c.ID == "" {
			t.Error("an unnamed conversation leaked into the chat list")
		}
	}
}

// An empty window is an empty list, not a nil one: the UI renders "no chats"
// from a list it can measure.
func TestChatsOnAnEmptyWindow(t *testing.T) {
	st := usageStore(t)
	rep, err := st.Chats(context.Background(), time.Now().Add(-time.Hour), 50)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Chats == nil {
		t.Error("Chats is nil; it must marshal as [] rather than null")
	}
	if len(rep.Chats) != 0 || rep.Total != 0 {
		t.Errorf("got %d chats, want none", len(rep.Chats))
	}
	if rep.Unattributed.Requests != 0 {
		t.Errorf("unattributed = %d, want 0", rep.Unattributed.Requests)
	}
}

func TestChatEventsAreOldestFirst(t *testing.T) {
	st := usageStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 17, 9, 14, 0, 0, time.UTC)

	for i, tok := range []int{10, 200, 3000} {
		record(t, st, UsageEvent{At: t0.Add(time.Duration(i) * time.Minute),
			ConversationID: "04c27413", Client: "Claude Code",
			Model: "claude-opus-5", Status: 200, InputTokens: tok})
	}
	record(t, st, UsageEvent{At: t0, ConversationID: "other", Model: "claude-opus-5",
		Status: 200, InputTokens: 1})

	events, err := st.ChatEvents(ctx, "04c27413", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want only this chat's 3", len(events))
	}
	// Read forwards: the shape worth seeing is context growing turn by turn.
	if events[0].InputTokens != 10 || events[2].InputTokens != 3000 {
		t.Errorf("order = %d..%d, want oldest first",
			events[0].InputTokens, events[2].InputTokens)
	}

	if _, err := st.ChatEvents(ctx, "", 100); err == nil {
		t.Error("an empty id must be refused, not answered with every unnamed request")
	}
}
