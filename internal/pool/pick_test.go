package pool

import (
	"testing"
	"time"

	"claudication/internal/store"
)

// account builds a candidate at a given utilisation, as a percentage — the
// units /api/oauth/usage reports. util < 0 means never fetched.
func account(id string, util float64, status string) store.Account {
	a := store.Account{ID: id, Email: id + "@example.com"}
	a.Quota.FiveHourUtil = util
	a.Quota.SevenDayUtil = -1
	a.Quota.FiveHourStatus = status
	return a
}

func newTestPool() *Pool {
	return &Pool{states: make(map[string]*health), now: time.Now}
}

// coolDown sits an account out without going through ReportFailure, which
// also writes to the store and the logger that a pick test does not have.
func (p *Pool) coolDown(id string, d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stateOf(id).cooldownUntil = p.now().Add(d)
}

// The order is the operator's, so the top account serves even when a lower one
// has more room. That is the whole point of a priority list.
func TestPickHonoursPriorityOverHeadroom(t *testing.T) {
	p := newTestPool()
	candidates := []store.Account{
		account("first", 80, "allowed"),
		account("second", 1, "allowed"),
	}

	for i := 0; i < 5; i++ {
		got, ok := p.pick(candidates)
		if !ok {
			t.Fatal("pick found nothing")
		}
		if got.ID != "first" {
			t.Fatalf("attempt %d picked %s, want first", i, got.ID)
		}
	}
}

// Once the subscription reports the top account locked, the next takes over.
func TestPickFallsThroughWhenTheTopIsOut(t *testing.T) {
	p := newTestPool()
	candidates := []store.Account{
		account("first", 100, "org_spend_cap_reached"),
		account("second", 20, "allowed"),
	}

	got, ok := p.pick(candidates)
	if !ok {
		t.Fatal("pick found nothing")
	}
	if got.ID != "second" {
		t.Errorf("picked %s, want second once the first is rejected", got.ID)
	}
}

// A utilisation figure at the very top of the window counts as out, even while
// nothing has locked the account yet: the figure is a poll rather than a live
// reading, and there is nothing useful left before the next one lands.
func TestPickSkipsAnExhaustedAccount(t *testing.T) {
	p := newTestPool()
	candidates := []store.Account{
		account("first", 99.5, "allowed"),
		account("second", 50, "allowed"),
	}

	got, _ := p.pick(candidates)
	if got.ID != "second" {
		t.Errorf("picked %s, want second", got.ID)
	}
}

// An account we have never heard from is unknown, not full. It must still be
// usable, or a freshly added account would never get its first request.
func TestPickUsesAnUnknownAccount(t *testing.T) {
	p := newTestPool()
	candidates := []store.Account{account("fresh", -1, "")}

	got, ok := p.pick(candidates)
	if !ok || got.ID != "fresh" {
		t.Fatalf("pick = %s, %v; want the unknown account", got.ID, ok)
	}
}

// An account that just failed sits out whatever its quota says, for as long as
// another one can serve.
func TestPickSkipsCoolingAccounts(t *testing.T) {
	p := newTestPool()
	candidates := []store.Account{
		account("first", 0, "allowed"),
		account("second", 50, "allowed"),
	}
	p.coolDown("first", time.Minute)

	got, ok := p.pick(candidates)
	if !ok {
		t.Fatal("pick found nothing")
	}
	if got.ID != "second" {
		t.Errorf("picked %s, want second while first cools down", got.ID)
	}
}

// If every account looks out of room, serve anyway: the figures are a snapshot
// and can be stale, and being refused by the upstream is a better outcome than
// refusing on its behalf.
func TestPickServesEvenWhenEverythingLooksExhausted(t *testing.T) {
	p := newTestPool()
	candidates := []store.Account{
		account("first", 100, "org_spend_cap_reached"),
		account("second", 100, "org_spend_cap_reached"),
	}

	got, ok := p.pick(candidates)
	if !ok {
		t.Fatal("pick refused on the pool's own initiative")
	}
	if got.ID != "first" {
		t.Errorf("picked %s, want the highest-priority one", got.ID)
	}
}

// Observed against the real upstream: it returned 429 and then served a 200
// half a second later. A rate-limit refusal is a statement about one request,
// not about the next minute — so with nothing else available, ask it rather
// than inventing a refusal the client cannot read a retry-after out of.
func TestPickServesWhenEverythingIsCooling(t *testing.T) {
	p := newTestPool()
	candidates := []store.Account{account("only", 0, "allowed")}
	p.coolDown("only", time.Minute)

	got, ok := p.pick(candidates)
	if !ok {
		t.Fatal("a sole cooling account turned a blip into an outage")
	}
	if got.ID != "only" {
		t.Errorf("picked %s", got.ID)
	}
}

// Cooling down still does its real job: it moves traffic off an account for as
// long as another one can take it.
func TestPickPrefersAHealthyAccountOverACoolingOne(t *testing.T) {
	p := newTestPool()
	candidates := []store.Account{
		account("first", 0, "allowed"),
		account("second", 50, "allowed"),
	}
	p.coolDown("first", time.Minute)

	got, _ := p.pick(candidates)
	if got.ID != "second" {
		t.Errorf("picked %s, want second while first cools", got.ID)
	}
}

// When every account is cooling, the one that recovers soonest is the best bet.
func TestPickTakesTheSoonestToRecover(t *testing.T) {
	p := newTestPool()
	candidates := []store.Account{
		account("first", 0, "allowed"),
		account("second", 0, "allowed"),
	}
	p.coolDown("first", 10*time.Minute)
	p.coolDown("second", 5*time.Second)

	got, ok := p.pick(candidates)
	if !ok {
		t.Fatal("found nothing")
	}
	if got.ID != "second" {
		t.Errorf("picked %s, want the one recovering soonest", got.ID)
	}
}

// Limited is what the admin page warns of: every account that would serve is
// out of room or cooling, and the first back is the soonest of them — the
// later of a cooldown and a full window's reset for each.
func TestLimitedOnlyWhenEveryAccountIsSidelined(t *testing.T) {
	p := newTestPool()
	reset := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	out := account("out", 100, "allowed")
	out.Quota.FiveHourReset = reset
	free := account("free", 20, "allowed")

	if limited, _ := p.limited([]store.Account{out, free}); limited {
		t.Fatal("one account with room is not limited")
	}
	limited, back := p.limited([]store.Account{out})
	if !limited || !back.Equal(reset) {
		t.Fatalf("got %v, back %v; want limited, back at the window's reset %v", limited, back, reset)
	}

	p.coolDown("free", time.Hour)
	limited, back = p.limited([]store.Account{out, free})
	if !limited {
		t.Fatal("a cooling account and a full one: limited")
	}
	if d := time.Until(back); d < 55*time.Minute || d > time.Hour {
		t.Fatalf("back in %v, want the cooldown's hour, the sooner of the two", d)
	}
	if limited, _ := p.limited(nil); limited {
		t.Fatal("no account at all is unavailable, not limited")
	}
}

// A model sitting out on an account passes the account over for that model
// alone: Google's quotas are per model, and the account still serves the
// rest.
func TestAModelCooldownIsTheModelsOnly(t *testing.T) {
	p := newTestPool()
	candidates := []store.Account{account("first", 0, "allowed"), account("second", 0, "allowed")}
	p.mu.Lock()
	p.stateOf("first").models = map[string]time.Time{"gemini-2.5-pro": p.now().Add(time.Hour)}
	p.mu.Unlock()

	serving := func(model string) func(string) string { return func(string) string { return model } }
	if got, _ := p.pickFor(candidates, serving("gemini-2.5-pro")); got.ID != "second" {
		t.Errorf("Pro picked %s, want second", got.ID)
	}
	if got, _ := p.pickFor(candidates, serving("gemini-2.5-flash")); got.ID != "first" {
		t.Errorf("Flash picked %s, want first", got.ID)
	}
	if got, _ := p.pick(candidates); got.ID != "first" {
		t.Errorf("no model picked %s, want first", got.ID)
	}
	// With every account out for the model, the one back soonest serves.
	p.mu.Lock()
	p.stateOf("second").models = map[string]time.Time{"gemini-2.5-pro": p.now().Add(2 * time.Hour)}
	p.mu.Unlock()
	if got, ok := p.pickFor(candidates, serving("gemini-2.5-pro")); !ok || got.ID != "first" {
		t.Errorf("all out picked %s, want first", got.ID)
	}
}
