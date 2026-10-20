package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"claudication/internal/service/clients"
)

// makeClient creates a client of a type with its type's slots under their
// names, each following its default.
func makeClient(t *testing.T, st *Store, name, typ string) Client {
	t.Helper()
	ty, _ := clients.Find(typ)
	var slots []NewSlot
	for _, a := range clients.Plan(nil, ty, ty.DefaultDialect()).Add {
		slots = append(slots, NewSlot{Slot: a.Slot, Name: a.Name, Binding: Binding{Follows: a.Follows}})
	}
	c, plain, err := st.CreateClient(context.Background(), name, typ, ty.DefaultDialect(), KeyLimits{}, slots)
	if err != nil || plain == "" {
		t.Fatalf("create %s: %v", name, err)
	}
	return c
}

// A client is its key: made together, read together, gone together, and its
// names are its own — two clients may share one, one client may not hold it
// twice.
func TestClientsOwnTheirNames(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	a, b := makeClient(t, st, "a", "crush"), makeClient(t, st, "b", "crush")
	if len(a.Slots) != 2 || a.Slots[0].Name != "large" || b.Slots[0].Name != "large" ||
		a.Slots[0].Follows != clients.FollowOpus || a.Mode != clients.Binding || a.Type != "crush" {
		t.Fatalf("a = %+v", a)
	}
	if k, _ := st.Key(ctx, a.ID); k.ClientType != "crush" || !k.NamesSlots() {
		t.Errorf("the key does not know its client: %+v", k)
	}
	// Bound apart: same name, two models.
	if err := st.SetSlot(ctx, a.ID, "large", "", Binding{Model: "claude-sonnet-5"}); err != nil {
		t.Fatal(err)
	}
	sa, _, _ := st.ClientSlotNamed(ctx, a.ID, "large")
	sb, _, _ := st.ClientSlotNamed(ctx, b.ID, "large")
	if sa.Model != "claude-sonnet-5" || sa.Follows != "" || sb.Follows != clients.FollowOpus {
		t.Errorf("a.large %+v, b.large %+v", sa, sb)
	}
	if _, ok, _ := st.ClientSlotNamed(ctx, a.ID, "medium"); ok {
		t.Error("a name the client does not have resolved")
	}
	// One client cannot hold a name twice; a bad name or binding is refused
	// in words.
	for _, c := range []struct {
		name string
		b    Binding
	}{
		{"small", Binding{Model: "x"}},
		{"Large One", Binding{Model: "x"}},
		{"", Binding{}},
		{"", Binding{Model: "x", Follows: clients.FollowOpus}},
		{"", Binding{Follows: "claude:"}},
	} {
		if err := st.SetSlot(ctx, a.ID, "large", c.name, c.b); !errors.Is(err, ErrInvalidClient) {
			t.Errorf("SetSlot(%q, %+v) = %v", c.name, c.b, err)
		}
	}
	if err := st.SetSlot(ctx, a.ID, "large", "big", Binding{Follows: clients.FollowFirst}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Client(ctx, a.ID); got.Slots[0].Name != "big" || got.Slots[0].Follows != clients.FollowFirst {
		t.Errorf("renamed slot %+v", got.Slots[0])
	}
	// A typed client's slots are its configuration's.
	if err := st.AddSlot(ctx, a.ID, "extra", Binding{Model: "x"}); !errors.Is(err, ErrInvalidClient) {
		t.Errorf("AddSlot on crush: %v", err)
	}
	if err := st.RemoveSlot(ctx, a.ID, "small"); !errors.Is(err, ErrInvalidClient) {
		t.Errorf("RemoveSlot on crush: %v", err)
	}
	if err := st.SetClientMode(ctx, a.ID, clients.Direct); !errors.Is(err, ErrInvalidClient) {
		t.Errorf("direct mode on crush: %v", err)
	}

	// Deleting the key deletes the client and its names.
	if err := st.DeleteKey(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Client(ctx, a.ID); !errors.Is(err, ErrClientNotFound) {
		t.Errorf("deleted client: %v", err)
	}
	var n int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM client_slots WHERE key_id = ?`, a.ID).Scan(&n)
	if n != 0 {
		t.Errorf("%d slots outlived their client", n)
	}
	all, _ := st.Clients(ctx)
	if len(all) != 1 || all[0].ID != b.ID {
		t.Errorf("clients after the delete: %+v", all)
	}
}

// slotNamed is the id of a client's slot by its name.
func slotNamed(t *testing.T, c Client, name string) string {
	t.Helper()
	for _, s := range c.Slots {
		if s.Name == name {
			return s.Slot
		}
	}
	t.Fatalf("no slot named %q in %+v", name, c.Slots)
	return ""
}

// A slot's id is not its name: renamed, the name it had is free for another
// slot of the same client.
func TestARenamedSlotFreesItsName(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	c := makeClient(t, st, "tool", "custom")
	if err := st.AddSlot(ctx, c.ID, "fast", Binding{Model: "claude-haiku-5"}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Client(ctx, c.ID)
	if err := st.SetSlot(ctx, c.ID, slotNamed(t, got, "fast"), "quick", Binding{Model: "claude-haiku-5"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddSlot(ctx, c.ID, "fast", Binding{Model: "claude-sonnet-5"}); err != nil {
		t.Fatalf("the freed name could not be given again: %v", err)
	}
	got, _ = st.Client(ctx, c.ID)
	if len(got.Slots) != 3 || slotNamed(t, got, "quick") == slotNamed(t, got, "fast") {
		t.Errorf("slots after the rename and the new one: %+v", got.Slots)
	}
}

// A custom client names its own slots and may be switched to direct mode and
// back, both kept.
func TestCustomClientModes(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	c := makeClient(t, st, "tool", "custom")
	if len(c.Slots) != 1 || c.Slots[0].Name != "large" {
		t.Fatalf("a custom client starts with %+v", c.Slots)
	}
	if err := st.AddSlot(ctx, c.ID, "small", Binding{Model: "claude-haiku-5"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddSlot(ctx, c.ID, "small", Binding{Model: "x"}); !errors.Is(err, ErrInvalidClient) {
		t.Errorf("a second small: %v", err)
	}
	if err := st.SetKeyModel(ctx, c.ID, "claude-opus-5", false); err != nil {
		t.Fatal(err)
	}
	if err := st.SetClientMode(ctx, c.ID, clients.Direct); err != nil {
		t.Fatal(err)
	}
	k, _ := st.Key(ctx, c.ID)
	if k.NamesSlots() || k.Allows("claude-opus-5") || !k.Allows("claude-haiku-5") {
		t.Errorf("direct mode: %+v", k)
	}
	if err := st.SetClientMode(ctx, c.ID, clients.Binding); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Client(ctx, c.ID)
	if len(got.Slots) != 2 || len(got.ModelsOff) != 1 || got.Mode != clients.Binding {
		t.Errorf("back in binding mode, both kept: %+v", got)
	}
	if err := st.RemoveSlot(ctx, c.ID, slotNamed(t, got, "small")); err != nil {
		t.Fatal(err)
	}
	if err := st.SetClientMode(ctx, c.ID, "sideways"); !errors.Is(err, ErrInvalidClient) {
		t.Errorf("no such mode: %v", err)
	}
}

// A change of type moves the slots the two types share, binding and all,
// adds the new type's others following their defaults and frees the rest;
// the key and its name stay, and a typed client is in binding mode.
// The review's case, applied: crush on Messages whose small slot is named
// gpt-5-large moves to Responses without a name clash.
func TestApplyChangeRenamesPastAKeptName(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	c := makeClient(t, st, "box", "crush")
	if err := st.SetSlot(ctx, c.ID, "small", "gpt-5-large", Binding{Follows: clients.FollowHaiku}); err != nil {
		t.Fatal(err)
	}
	c, _ = st.Client(ctx, c.ID)
	if err := st.ApplyChange(ctx, c.ID, clients.Plan(c.Existing(), clients.Crush, clients.Responses)); err != nil {
		t.Fatalf("crush to Responses: %v", err)
	}
	got, _ := st.Client(ctx, c.ID)
	if slotNamed(t, got, "gpt-5-large") != "small" || slotNamed(t, got, "gpt-5-large-2") != "large" {
		t.Errorf("slots %+v", got.Slots)
	}
}

func TestApplyChange(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	c := makeClient(t, st, "box", "crush")
	if err := st.SetSlot(ctx, c.ID, "small", "", Binding{Model: "gpt-6-luna"}); err != nil {
		t.Fatal(err)
	}
	ch := clients.Plan(c.Existing(), clients.Opencode, clients.Messages)
	if err := st.ApplyChange(ctx, c.ID, ch); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Client(ctx, c.ID)
	if got.Type != "opencode" || got.Dialect != clients.Messages || got.Name != "box" || len(got.Slots) != 2 ||
		got.Slots[0].Slot != "main" || got.Slots[0].Name != "claude-large" ||
		got.Slots[1].Slot != "small" || got.Slots[1].Name != "claude-small" || got.Slots[1].Model != "gpt-6-luna" {
		t.Errorf("crush → opencode: %+v", got)
	}
	// To Claude Code, from a custom client in direct mode: binding mode, the
	// one slot kept as the main one, the rest added.
	cu := makeClient(t, st, "tool", "custom")
	if err := st.SetClientMode(ctx, cu.ID, clients.Direct); err != nil {
		t.Fatal(err)
	}
	cu, _ = st.Client(ctx, cu.ID)
	if err := st.ApplyChange(ctx, cu.ID, clients.Plan(cu.Existing(), clients.ClaudeCode, clients.Messages)); err != nil {
		t.Fatal(err)
	}
	got, _ = st.Client(ctx, cu.ID)
	if got.Mode != clients.Binding || len(got.Slots) != len(clients.ClaudeCode.Slots) ||
		got.Slots[0].Name != "claude-large" || got.Slots[1].Name != "claude-small" ||
		got.Slots[1].Follows != clients.FollowHaiku {
		t.Errorf("custom → claude-code: %+v", got)
	}
	if err := st.ApplyChange(ctx, cu.ID, clients.Change{Type: "codex", Dialect: clients.Messages}); !errors.Is(err, ErrInvalidClient) {
		t.Errorf("codex on Messages: %v", err)
	}
}

// Disabled reads with the key, so the request path refuses on the lookup it
// already makes; configured is set once, by the first use, and a new secret
// clears it.
func TestClientSwitchesAndConfigured(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	c := makeClient(t, st, "box", "crush")
	if err := st.SetClientDisabled(ctx, c.ID, true); err != nil {
		t.Fatal(err)
	}
	if k, _ := st.Key(ctx, c.ID); !k.Disabled {
		t.Error("the disabled client's key does not say so")
	}
	if err := st.SetClientDisabled(ctx, c.ID, false); err != nil {
		t.Fatal(err)
	}
	if k, _ := st.Key(ctx, c.ID); k.Disabled {
		t.Error("switched back on, still disabled")
	}
	if got, _ := st.Client(ctx, c.ID); got.ConfiguredAt != nil {
		t.Error("configured before any use")
	}
	st.TouchKey(ctx, c.ID)
	first, _ := st.Client(ctx, c.ID)
	if first.ConfiguredAt == nil {
		t.Fatal("not configured by its first use")
	}
	if err := st.MarkConfigured(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if again, _ := st.Client(ctx, c.ID); !again.ConfiguredAt.Equal(*first.ConfiguredAt) {
		t.Error("configured moved on a second use")
	}
	if _, _, err := st.RotateKey(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Client(ctx, c.ID); got.ConfiguredAt != nil {
		t.Error("a new secret left the client configured")
	}
	if err := st.SetClientDisabled(ctx, "nope", true); !errors.Is(err, ErrClientNotFound) {
		t.Errorf("an unknown client: %v", err)
	}
}

// One client's or one account's traffic, by model name and the model that
// served it, inside the window only; KeyRequests is every request a key sent,
// refusals included, the gateway's own left out.
func TestSlotUsageAndKeyRequests(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	a, b := makeClient(t, st, "a", "crush"), makeClient(t, st, "b", "codex")
	now := time.Now()
	rec := func(at time.Time, key, account, slot, model string, rejected bool, path string) {
		t.Helper()
		in, out := 10, 5
		if rejected {
			in, out = 0, 0
		}
		if err := st.RecordUsage(ctx, UsageEvent{At: at, KeyID: key, AccountID: account, Provider: "anthropic",
			VirtualModel: slot, Model: model, Path: path, Status: 200, InputTokens: in, OutputTokens: out,
			Rejected: rejected}); err != nil {
			t.Fatal(err)
		}
	}

	rec(now.Add(-time.Hour), a.ID, "acc1", "large", "claude-opus-5", false, "/v1/messages")
	rec(now.Add(-2*time.Hour), a.ID, "acc1", "large", "claude-opus-5", false, "/v1/messages")
	rec(now.Add(-3*time.Hour), a.ID, "acc2", "small", "claude-haiku-5", false, "/v1/messages")
	rec(now.Add(-10*24*time.Hour), a.ID, "acc1", "large", "claude-opus-5", false, "/v1/messages")
	rec(now.Add(-time.Hour), b.ID, "acc1", "large", "gpt-6-luna", false, "/v1/responses")
	rec(now.Add(-time.Minute), a.ID, "", "", "", true, "/v1/messages")
	rec(now.Add(-time.Minute), "titler", "acc1", "", "claude-haiku-5", false, InternalPath)

	week := now.AddDate(0, 0, -7)
	rows, err := st.SlotUsage(ctx, SlotScope{KeyID: a.ID}, week)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Slot != "large" || rows[0].Requests != 2 || rows[0].InputTokens != 20 ||
		rows[0].Name != "a" || rows[0].Type != "crush" || rows[1].Slot != "small" {
		t.Errorf("client a by slot: %+v", rows)
	}
	rows, err = st.SlotUsage(ctx, SlotScope{AccountID: "acc1"}, week)
	if err != nil {
		t.Fatal(err)
	}
	// The gateway's own titling on the account is there, under its own key.
	if len(rows) != 3 || rows[0].KeyID != a.ID || rows[0].Requests != 2 || rows[1].KeyID != b.ID || rows[2].KeyID != "titler" ||
		rows[1].Model != "gpt-6-luna" {
		t.Errorf("account acc1 by client: %+v", rows)
	}
	if rows, _ := st.SlotUsage(ctx, SlotScope{KeyID: a.ID}, now.AddDate(0, 0, -30)); len(rows) != 2 || rows[0].Requests != 3 {
		t.Errorf("30 days: %+v", rows)
	}
	if _, err := st.SlotUsage(ctx, SlotScope{}, week); err == nil {
		t.Error("a scope naming nothing was read")
	}

	reqs, err := st.KeyRequests(ctx, a.ID, week)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 4 || !reqs[0].At.Before(reqs[3].At) || reqs[3].Tokens != 0 {
		t.Errorf("a's requests: %+v", reqs)
	}
}
