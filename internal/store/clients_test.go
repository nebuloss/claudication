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
	for _, a := range ty.Initial() {
		slots = append(slots, NewSlot{Slot: a.Slot, Name: a.Name, Binding: Binding{Follows: a.Follows}})
	}
	c, plain, err := st.CreateClient(context.Background(), name, typ, KeyLimits{}, slots)
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
	if err := st.SetSlot(ctx, a.ID, "large", "big", Binding{Follows: clients.FollowSol}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Client(ctx, a.ID); got.Slots[0].Name != "big" || got.Slots[0].Follows != clients.FollowSol {
		t.Errorf("renamed slot %+v", got.Slots[0])
	}
	// crush takes names of the operator's own, for its picker, beside its
	// two settings, which stay.
	if err := st.AddSlot(ctx, a.ID, "extra", Binding{Model: "x"}); err != nil {
		t.Errorf("AddSlot on crush: %v", err)
	}
	if err := st.AddSlot(ctx, a.ID, "small", Binding{Model: "x"}); !errors.Is(err, ErrInvalidClient) {
		t.Errorf("AddSlot on crush under a name it has: %v", err)
	}
	if err := st.RemoveSlot(ctx, a.ID, "small"); !errors.Is(err, ErrInvalidClient) {
		t.Errorf("RemoveSlot of crush's small: %v", err)
	}
	withExtra, _ := st.Client(ctx, a.ID)
	extra := slotNamed(t, withExtra, "extra")
	if len(withExtra.Slots) != 3 || extra == "large" || extra == "small" {
		t.Fatalf("crush's slots after adding one: %+v", withExtra.Slots)
	}
	if err := st.RemoveSlot(ctx, a.ID, extra); err != nil {
		t.Errorf("RemoveSlot of crush's own name: %v", err)
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

// A slot takes any valid name, whatever its type: no client needs a
// provider's word in it (internal/service/clients). Only the name's own rule
// holds, and a rebind keeps the name it has.
func TestASlotTakesAPlainName(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	for _, typ := range []string{"claude-code", "opencode"} {
		c := makeClient(t, st, "laptop-"+typ, typ)
		if err := st.SetSlot(ctx, c.ID, "main", "big", Binding{Follows: clients.FollowOpus}); err != nil {
			t.Errorf("%s: a plain name: %v", typ, err)
		}
		if err := st.SetSlot(ctx, c.ID, "main", "Big Model", Binding{Follows: clients.FollowOpus}); !errors.Is(err, ErrInvalidClient) {
			t.Errorf("%s: a name a config cannot carry: %v", typ, err)
		}
		if err := st.SetSlot(ctx, c.ID, "main", "", Binding{Model: "claude-sonnet-5"}); err != nil {
			t.Errorf("%s: a rebind: %v", typ, err)
		}
		got, err := st.Client(ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if slotNamed(t, got, "big") != "main" {
			t.Errorf("%s: slots after the rename %+v", typ, got.Slots)
		}
		if err := st.RemoveSlot(ctx, c.ID, "main"); !errors.Is(err, ErrInvalidClient) {
			t.Errorf("%s: RemoveSlot: %v", typ, err)
		}
	}
}

// Claude Code, Codex and opencode take names of the operator's own for their
// pickers, as crush does: added beside the type's settings, under a name
// none of them has, and removed again; the settings themselves stay.
func TestPickerNamesOnEveryTypedClient(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	for _, typ := range []string{"claude-code", "codex", "opencode"} {
		c := makeClient(t, st, "box-"+typ, typ)
		if err := st.AddSlot(ctx, c.ID, "deep", Binding{Model: "gpt-5.5"}); err != nil {
			t.Fatalf("%s: AddSlot: %v", typ, err)
		}
		if err := st.AddSlot(ctx, c.ID, "large", Binding{Model: "x"}); !errors.Is(err, ErrInvalidClient) {
			t.Errorf("%s: AddSlot under a name it has: %v", typ, err)
		}
		got, err := st.Client(ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		id := slotNamed(t, got, "deep")
		if spec, ok := got.Spec().SlotFor(id); !ok || spec.Role != clients.RolePicker || spec.Field != "" {
			t.Errorf("%s: the added name reads as %+v, %v", typ, spec, ok)
		}
		for _, s := range got.Spec().Slots {
			if err := st.RemoveSlot(ctx, c.ID, s.ID); !errors.Is(err, ErrInvalidClient) {
				t.Errorf("%s: RemoveSlot of its own %s: %v", typ, s.ID, err)
			}
		}
		if err := st.RemoveSlot(ctx, c.ID, id); err != nil {
			t.Errorf("%s: RemoveSlot of the added name: %v", typ, err)
		}
		if err := st.SetClientMode(ctx, c.ID, clients.Direct); !errors.Is(err, ErrInvalidClient) {
			t.Errorf("%s: direct mode: %v", typ, err)
		}
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

// An agent may manage its client only once the operator says so, and the key
// the MCP server authenticates with carries the switch.
func TestClientAgentManageSwitch(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	c := makeClient(t, st, "box", "claude-code")
	if k, _ := st.Key(ctx, c.ID); k.AgentManage {
		t.Fatal("a new client lets its agent manage it")
	}
	if err := st.SetClientAgentManage(ctx, c.ID, true); err != nil {
		t.Fatal(err)
	}
	if k, _ := st.Key(ctx, c.ID); !k.AgentManage {
		t.Error("switched on, the key does not say so")
	}
	if err := st.SetClientAgentManage(ctx, c.ID, false); err != nil {
		t.Fatal(err)
	}
	if k, _ := st.Key(ctx, c.ID); k.AgentManage {
		t.Error("switched off, still on")
	}
	if err := st.SetClientAgentManage(ctx, "nope", true); !errors.Is(err, ErrClientNotFound) {
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
	// The whole gateway, split by account as well: client a's "large" on acc1
	// is one row, its "small" on acc2 another, and the scoped reads never
	// carried an account.
	rows, err = st.SlotUsage(ctx, SlotScope{Gateway: true}, week)
	if err != nil {
		t.Fatal(err)
	}
	// The three single requests tie, and ties go by key id, which is random:
	// found by what they are rather than where they land.
	var sum int64
	var small bool
	for _, r := range rows {
		sum += r.Requests
		small = small || (r.KeyID == a.ID && r.AccountID == "acc2" && r.Slot == "small")
	}
	if len(rows) != 4 || rows[0].KeyID != a.ID || rows[0].AccountID != "acc1" || rows[0].Requests != 2 || !small {
		t.Errorf("gateway by client and account: %+v", rows)
	}
	if sum != 5 {
		t.Errorf("gateway rows add up to %d requests, want the 5 relayed this week", sum)
	}
	if rows, _ := st.SlotUsage(ctx, SlotScope{KeyID: a.ID}, week); rows[0].AccountID != "" {
		t.Errorf("a client's own read split by account: %+v", rows)
	}

	// By the minute, refusals included, oldest first: two an hour ago, two a
	// minute ago (the refusal and the titling).
	minutes, err := st.RequestsPerMinute(ctx, now.Add(-90*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(minutes) != 2 || minutes[0].Requests != 2 || minutes[1].Requests != 2 ||
		!minutes[0].Minute.Before(minutes[1].Minute) ||
		!minutes[1].Minute.Equal(now.Add(-time.Minute).Truncate(time.Minute)) {
		t.Errorf("requests per minute: %+v", minutes)
	}

	reqs, err := st.KeyRequests(ctx, a.ID, week)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 4 || !reqs[0].At.Before(reqs[3].At) || reqs[3].Tokens != 0 {
		t.Errorf("a's requests: %+v", reqs)
	}
}

// What the MCP server's status counts, in SQL: requests in the period,
// refusals included, tokens over the day, an agent's changes neither.
func TestKeyActivity(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	c := makeClient(t, st, "box", "crush")
	now := time.Now()
	for _, e := range []UsageEvent{
		{At: now.Add(-30 * time.Second), KeyID: c.ID, Path: "/v1/messages", Status: 200, InputTokens: 10, OutputTokens: 5},
		{At: now.Add(-10 * time.Second), KeyID: c.ID, Path: "/v1/messages", Status: 429, Rejected: true},
		{At: now.Add(-5 * time.Second), KeyID: c.ID, Path: "/mcp/bind_model", Status: 200, Change: true},
		{At: now.Add(-2 * time.Hour), KeyID: c.ID, Path: "/v1/messages", Status: 200, InputTokens: 100},
		{At: now.Add(-48 * time.Hour), KeyID: c.ID, Path: "/v1/messages", Status: 200, InputTokens: 1000},
		{At: now.Add(-time.Second), KeyID: "someone", Path: "/v1/messages", Status: 200, InputTokens: 7},
	} {
		if err := st.RecordUsage(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	reqs, tokens, err := st.KeyActivity(ctx, c.ID, now.Add(-time.Minute), now.Add(-24*time.Hour))
	if err != nil || reqs != 2 || tokens != 115 {
		t.Errorf("activity: %d requests, %d tokens, %v", reqs, tokens, err)
	}
	// A change is read back as one, and counted in no usage figure.
	use, _ := st.KeyUsageOf(ctx, c.ID, now.Add(-time.Hour))
	if use[c.ID].Requests != 1 {
		t.Errorf("usage counts %d requests, want the one relayed", use[c.ID].Requests)
	}
	rows, _, err := st.RecentUsage(ctx, 10, UsageCursor{}, RequestFilter{KeyIDs: []string{c.ID}, Kinds: []string{"change"}})
	if err != nil || len(rows) != 1 || !rows[0].Change || rows[0].Rejected {
		t.Errorf("the change as the log reads it: %+v %v", rows, err)
	}
}
