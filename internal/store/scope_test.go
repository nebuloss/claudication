package store

import (
	"context"
	"testing"
	"time"
)

// A detail page reads the same report narrowed to its account or key, and the
// chat list narrowed to the chats that account served or that key paid for —
// each shown whole, not just the requests that matched.
func TestUsageAndChatsNarrowToOneAccountOrKey(t *testing.T) {
	st := usageStore(t)
	ctx := context.Background()
	t0 := time.Now().Add(-time.Minute)

	// chat-a starts on a1 and is moved to a2 by the pool; chat-b is all a2,
	// paid for by another key.
	record(t, st, UsageEvent{At: t0, ConversationID: "chat-a", KeyID: "k1", KeyName: "laptop",
		AccountID: "a1", AccountEmail: "one@example.com", Model: "claude-opus-5", Status: 200, InputTokens: 100})
	record(t, st, UsageEvent{At: t0.Add(time.Second), ConversationID: "chat-a", KeyID: "k1", KeyName: "laptop",
		AccountID: "a2", AccountEmail: "two@example.com", Model: "claude-opus-5", Status: 200, InputTokens: 10})
	record(t, st, UsageEvent{At: t0.Add(2 * time.Second), ConversationID: "chat-b", KeyID: "k2", KeyName: "ci",
		AccountID: "a2", AccountEmail: "two@example.com", Model: "claude-sonnet-5", Status: 500, Error: "boom", InputTokens: 1})

	since := t0.Add(-time.Hour)
	rep, err := st.UsageIn(ctx, since, UsageScope{AccountID: "a1"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Totals.Requests != 1 || rep.Totals.InputTokens != 100 || len(rep.ByKey) != 1 || rep.ByKey[0].ID != "k1" {
		t.Errorf("a1: %+v, by key %+v", rep.Totals, rep.ByKey)
	}
	rep, _ = st.UsageIn(ctx, since, UsageScope{KeyID: "k2"})
	if rep.Totals.Requests != 1 || rep.Totals.Errors != 1 || len(rep.ByAccount) != 1 || rep.ByAccount[0].ID != "a2" {
		t.Errorf("k2: %+v, by account %+v", rep.Totals, rep.ByAccount)
	}
	rep, _ = st.UsageIn(ctx, since, UsageScope{AccountID: "a2", KeyID: "k1"})
	if rep.Totals.Requests != 1 || rep.Totals.InputTokens != 10 {
		t.Errorf("a2 and k1: %+v", rep.Totals)
	}
	whole, _ := st.UsageIn(ctx, since, UsageScope{})
	if whole.Totals.Requests != 3 {
		t.Errorf("unscoped: %d requests, want 3", whole.Totals.Requests)
	}

	chats, err := st.ChatsIn(ctx, since, 10, UsageScope{AccountID: "a1"})
	if err != nil {
		t.Fatal(err)
	}
	if chats.Total != 1 || len(chats.Chats) != 1 || chats.Chats[0].ID != "chat-a" || chats.Chats[0].Requests != 2 {
		t.Errorf("a1's chats: total %d, %+v; want chat-a whole, both requests", chats.Total, chats.Chats)
	}
	chats, _ = st.ChatsIn(ctx, since, 10, UsageScope{AccountID: "a2"})
	if chats.Total != 2 {
		t.Errorf("a2 served %d chats, want both", chats.Total)
	}
	chats, _ = st.ChatsIn(ctx, since, 10, UsageScope{KeyID: "nobody"})
	if chats.Total != 0 || len(chats.Chats) != 0 {
		t.Errorf("a key with no traffic has chats: %+v", chats)
	}

	// The request log narrows by account the way it already did by key.
	events, _, err := st.RecentUsage(ctx, 10, UsageCursor{}, RequestFilter{AccountIDs: []string{"a1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].AccountID != "a1" {
		t.Errorf("log for a1: %+v", events)
	}
}
