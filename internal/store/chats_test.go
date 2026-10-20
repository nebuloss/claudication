package store

import (
	"context"
	"reflect"
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
		KeyID: "k1", KeyName: "laptop", AccountID: "a1", AccountEmail: "a@example.com", Model: "claude-opus-5",
		Status: 200, InputTokens: 100, OutputTokens: 10, CacheReadTokens: 900})
	record(t, st, UsageEvent{At: t0.Add(time.Minute), ConversationID: "04c27413",
		Client: "Claude Code", KeyID: "k1", KeyName: "laptop", AccountID: "a1", AccountEmail: "a@example.com",
		Model: "claude-opus-5", Status: 429, Error: "refused"})
	record(t, st, UsageEvent{At: t0.Add(2 * time.Minute), ConversationID: "04c27413",
		Client: "Claude Code", KeyID: "k1", KeyName: "laptop", AccountID: "a1", AccountEmail: "a@example.com",
		Model: "claude-sonnet-5", Status: 200, InputTokens: 50, OutputTokens: 5})

	// A cheaper opencode chat, so ordering by spend is observable.
	record(t, st, UsageEvent{At: t0.Add(time.Hour), ConversationID: "ses_f50990",
		Client: "opencode", KeyID: "k1", KeyName: "laptop", AccountID: "a1", AccountEmail: "a@example.com",
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

// The gateway's own requests are part of what a chat cost and say nothing
// about who was having it.
//
// The bug: naming a chat records a usage row against that conversation, and
// the rollup picked its client with MAX(client). A lowercase "g" sorts above
// "Charm-Crush", so a crush chat was attributed to the gateway. The key name
// went the same way, since key and account take the latest row and the title
// call is always the latest.
func TestTheGatewaysOwnRequestsDoNotRenameTheChat(t *testing.T) {
	st := usageStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)

	record(t, st, UsageEvent{At: t0, ConversationID: "457c888e", Client: "Charm-Crush",
		KeyID: "k1", KeyName: "laptop", AccountID: "a1", AccountEmail: "a@example.com", Model: "claude-opus-5",
		Path: "/v1/messages", Status: 200, InputTokens: 100, OutputTokens: 20})
	// Later than every real turn, which is what made it win.
	record(t, st, UsageEvent{At: t0.Add(time.Minute), ConversationID: "457c888e",
		KeyID: "own", KeyName: "gateway (internal)", AccountID: "a1", AccountEmail: "a@example.com",
		Model: "claude-opus-5", Path: InternalPath, Status: 200,
		InputTokens: 4, OutputTokens: 9, CacheReadTokens: 11406})

	rep, err := st.Chats(ctx, t0.Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Chats) != 1 {
		t.Fatalf("got %d chats, want 1", len(rep.Chats))
	}
	got := rep.Chats[0]

	if got.Client != "Charm-Crush" {
		t.Errorf("client = %q, want the client that was actually talking", got.Client)
	}
	if got.KeyName != "laptop" {
		t.Errorf("key = %q, want the key the client used", got.KeyName)
	}
	// Counted, though: naming a chat is part of what that chat cost, and
	// hiding it would leave a total that does not add up to the Overview's.
	if got.Requests != 2 {
		t.Errorf("requests = %d, want both turns counted", got.Requests)
	}
	if got.Tokens() != 11539 {
		t.Errorf("tokens = %d, want the naming call included", got.Tokens())
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

// Every field written through RecordUsage comes back through ChatEvents.
//
// ChatEvents once kept its own column list, which stopped at error: the chat
// view sent request rows with error_code, ip, virtual_model and rejected always
// empty, while the request log sent the same rows whole. The check walks the
// struct, so a field added later and left out of the shared list fails here
// rather than in the UI.
func TestChatEventsReturnEveryRecordedField(t *testing.T) {
	st := usageStore(t)
	ctx := context.Background()

	want := UsageEvent{
		At:    time.Date(2026, 10, 19, 9, 14, 3, 250_000_000, time.UTC),
		KeyID: "k1", KeyName: "laptop",
		AccountID: "a1", AccountEmail: "op@example.com", Provider: "anthropic",
		Model: "claude-opus-5", VirtualModel: "claude-large",
		Path: "/v1/messages", ConversationID: "c1", Client: "Claude Code",
		Status: 429, Streaming: true,
		InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, CacheWriteTokens: 4, ReasoningTokens: 1,
		Duration: 1500 * time.Millisecond, FirstToken: 250 * time.Millisecond,
		Error: "slow down", ErrorCode: "rate_limit_error",
		Rejected: true, IP: "192.0.2.7",
	}
	v := reflect.ValueOf(want)
	for i := range v.NumField() {
		// Naming is written as the row's kind and not read back as itself.
		if name := v.Type().Field(i).Name; name != "ID" && name != "Naming" && v.Field(i).IsZero() {
			t.Fatalf("fixture leaves %s at its zero value, so its loss would go unnoticed", name)
		}
	}
	record(t, st, want)

	events, err := st.ChatEvents(ctx, "c1", 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("ChatEvents = %d events, %v; want 1", len(events), err)
	}
	got := events[0]
	if got.ID == 0 {
		t.Error("ID was not read back")
	}
	if !got.At.Equal(want.At) {
		t.Errorf("At = %v, want %v", got.At, want.At)
	}
	got.ID, got.At = 0, want.At
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip lost fields:\n got %+v\nwant %+v", got, want)
	}
}

// A chat is labelled with how its client was run, from its latest relayed
// request that said: what tells a headless Claude Code chat, never named, from
// an interactive one not named yet. The gateway's own titling request says
// nothing about who was talking, and a request that carried no surface does
// not blank one that did.
func TestChatSurfaceIsTheLatestRelayedOne(t *testing.T) {
	st := usageStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

	record(t, st, UsageEvent{At: t0, ConversationID: "headless", Client: "Claude Code",
		Surface: "cli", Model: "claude-opus-5", Status: 200, InputTokens: 10})
	record(t, st, UsageEvent{At: t0.Add(time.Minute), ConversationID: "headless", Client: "Claude Code",
		Surface: "sdk-cli", Model: "claude-opus-5", Status: 200, InputTokens: 10})
	record(t, st, UsageEvent{At: t0.Add(2 * time.Minute), ConversationID: "headless", Client: "Claude Code",
		Model: "claude-opus-5", Status: 200, InputTokens: 10})
	record(t, st, UsageEvent{At: t0.Add(3 * time.Minute), ConversationID: "headless", Path: InternalPath,
		Surface: "sdk-ts/titler", Model: "claude-haiku-5", Status: 200, InputTokens: 1})

	record(t, st, UsageEvent{At: t0, ConversationID: "sdk", Client: "Claude Code",
		Surface: "sdk-py/nightly", Model: "claude-opus-5", Status: 200, InputTokens: 5})
	record(t, st, UsageEvent{At: t0, ConversationID: "ses_1", Client: "opencode",
		Model: "claude-opus-5", Status: 200, InputTokens: 1})

	rep, err := st.Chats(ctx, t0.Add(-time.Hour), 50)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range rep.Chats {
		got[c.ID] = c.Surface
	}
	want := map[string]string{
		"headless": "Headless (claude -p)",
		"sdk":      "Agent SDK (Python) · nightly",
		"ses_1":    "",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("surfaces = %v, want %v", got, want)
	}
	if rep.Unattributed.Surface != "" {
		t.Errorf("unattributed surface = %q", rep.Unattributed.Surface)
	}

	// And each request carries its own, as stored.
	events, err := st.ChatEvents(ctx, "headless", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 || events[0].Surface != "cli" || events[1].Surface != "sdk-cli" || events[2].Surface != "" {
		t.Errorf("events = %+v", events)
	}
}
