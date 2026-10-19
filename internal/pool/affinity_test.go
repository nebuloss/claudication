package pool

import (
	"context"
	"testing"
	"time"

	"claudication/internal/provider"
	"claudication/internal/store"
)

// connectAs adds an account of the given provider, as connect does for
// Anthropic.
func connectAs(t *testing.T, p *Pool, prov, email string) string {
	t.Helper()
	a, err := p.store.UpsertAccount(context.Background(),
		store.Account{Provider: prov, Email: email, AccountUUID: "uuid-" + email, ExpiresAt: fresh()},
		store.Tokens{AccessToken: "access-" + email, RefreshToken: "refresh-" + email})
	if err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}
	return a.ID
}

// A conversation that failed over stays where it landed: the next turn goes
// to the account that served the last one, not back to the top of the list,
// because its encrypted reasoning was made there.
func TestAffinityKeepsAConversationOnItsAccount(t *testing.T) {
	p, _ := fixture(t)
	p.PreferLastAccount("openai")
	first := connectAs(t, p, "openai", "first@example.com")
	second := connectAs(t, p, "openai", "second@example.com")
	ctx := WithConversation(context.Background(), "chat-1")

	// The first turn fails over to the second account.
	if lease, err := p.Acquire(ctx, "openai", "", map[string]bool{first: true}); err != nil || lease.Account.ID != second {
		t.Fatalf("failover = %s, %v; want the second account", lease.Account.ID, err)
	}
	// The first is healthy again, and still the next turn stays put.
	if lease, err := p.Acquire(ctx, "openai", "", nil); err != nil || lease.Account.ID != second {
		t.Errorf("next turn = %s, %v; want the account that served the last one", lease.Account.ID, err)
	}
	// Another conversation starts at the top of the list as ever.
	other := WithConversation(context.Background(), "chat-2")
	if lease, err := p.Acquire(other, "openai", "", nil); err != nil || lease.Account.ID != first {
		t.Errorf("a new conversation = %s, %v; want the first account", lease.Account.ID, err)
	}
}

// A preference, not a pin: the account that served last is passed over while
// it cools down, as any other would be.
func TestAffinityGivesWayToACooldown(t *testing.T) {
	p, _ := fixture(t)
	p.PreferLastAccount("openai")
	first := connectAs(t, p, "openai", "first@example.com")
	second := connectAs(t, p, "openai", "second@example.com")
	ctx := WithConversation(context.Background(), "chat-1")

	if _, err := p.Acquire(ctx, "openai", "", map[string]bool{first: true}); err != nil {
		t.Fatal(err)
	}
	p.ReportFailure(second, FailureRateLimit, "429")
	if lease, err := p.Acquire(ctx, "openai", "", nil); err != nil || lease.Account.ID != first {
		t.Errorf("= %s, %v; want the healthy account while the last one cools", lease.Account.ID, err)
	}
}

// Only a provider that asked for it keeps affinity: Anthropic's conversations
// follow the priority order, as they always have.
func TestAffinityIsPerProvider(t *testing.T) {
	p, _ := fixture(t)
	p.PreferLastAccount("openai")
	first := connect(t, p, "first@example.com", fresh())
	second := connect(t, p, "second@example.com", fresh())
	ctx := WithConversation(context.Background(), "chat-1")

	if _, err := p.Acquire(ctx, "anthropic", "", map[string]bool{first: true}); err != nil {
		t.Fatal(err)
	}
	if lease, err := p.Acquire(ctx, "anthropic", "", nil); err != nil || lease.Account.ID != first {
		t.Errorf("= %s, %v; want the first account: Anthropic keeps no affinity (second was %s)",
			lease.Account.ID, err, second)
	}
}

// A reset the upstream named sits the account out until then, past the
// backoff's minute; and one absurdly far out is held to a week and a day.
func TestReportFailureUntilHonoursTheReset(t *testing.T) {
	p, _ := fixture(t)
	base := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return base }
	id := connectAs(t, p, "openai", "a@example.com")

	reset := base.Add(3 * time.Hour)
	p.ReportFailureUntil(id, FailureRateLimit, "usage_limit_reached", reset)
	if got := p.stateOf(id).cooldownUntil; !got.Equal(reset) {
		t.Errorf("cooling until %s, want the reset %s", got, reset)
	}

	p.ReportFailureUntil(id, FailureRateLimit, "usage_limit_reached", base.Add(365*24*time.Hour))
	if got := p.stateOf(id).cooldownUntil; !got.Equal(base.Add(maxReportedWait)) {
		t.Errorf("cooling until %s, want it held to %s", got, maxReportedWait)
	}

	// A reset already past leaves the ordinary backoff.
	p.ReportFailureUntil(id, FailureRateLimit, "usage_limit_reached", base.Add(-time.Hour))
	if got := p.stateOf(id).cooldownUntil; !got.After(base) {
		t.Errorf("cooling until %s, want the backoff after %s", got, base)
	}
}

// A relayed answer's quota is stored by window length, and a full window takes
// the account out of rotation at once.
func TestNoteQuotaStoresTheWindowsByLength(t *testing.T) {
	p, st := fixture(t)
	id := connectAs(t, p, "openai", "a@example.com")
	reset := time.Date(2026, 10, 19, 17, 0, 0, 0, time.UTC)

	// Secondary first: the columns are chosen by length, not position.
	p.NoteQuota(id, provider.Quota{Windows: []provider.Window{
		{Seconds: 604800, UsedPercent: 12},
		{Seconds: 18000, UsedPercent: 100, ResetAt: reset},
	}})
	a, err := st.Account(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if a.Quota.FiveHourUtil != 100 || a.Quota.SevenDayUtil != 12 || !a.Quota.FiveHourReset.Equal(reset) {
		t.Errorf("quota = %+v", a.Quota)
	}
	if !outOfRoom(a) {
		t.Error("a full 5-hour window left the account in rotation")
	}

	// limited closes the account even when no figure reads full.
	p.quotaSeen = map[string]quotaNote{}
	p.NoteQuota(id, provider.Quota{Limited: true, Windows: []provider.Window{{Seconds: 18000, UsedPercent: 40}}})
	if a, _ = st.Account(context.Background(), id); a.Quota.Allowed() {
		t.Errorf("limited quota reads as allowed: %+v", a.Quota)
	}
}

// A report with no windows — a refusal that carried none of its headers —
// leaves the figures known as they were, rather than reading as every window
// unknown and closed.
func TestNoteQuotaWithoutWindowsKeepsTheFigures(t *testing.T) {
	p, st := fixture(t)
	id := connectAs(t, p, "openai", "a@example.com")
	p.NoteQuota(id, provider.Quota{Windows: []provider.Window{{Seconds: 18000, UsedPercent: 40}, {Seconds: 604800, UsedPercent: 9}}})
	p.quotaSeen = map[string]quotaNote{}
	p.NoteQuota(id, provider.Quota{Limited: true})
	a, err := st.Account(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if a.Quota.FiveHourUtil != 40 || a.Quota.SevenDayUtil != 9 || !a.Quota.Allowed() {
		t.Errorf("quota after an empty report = %+v", a.Quota)
	}
}
