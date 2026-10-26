package pool

import (
	"context"
	"errors"
	"testing"
)

// A family is served per account: an account on a plan without the newest
// version serves the newest it lists, the accounts that have the newest are
// preferred while one of them can take the request, and the request is
// refused only when no account has any version.
func TestAcquireServesEachAccountsNewestVersion(t *testing.T) {
	ctx := context.Background()
	p, st := fixture(t)
	// First in the operator's order, and on the plan without the newest.
	older := connect(t, p, "older@example.com", fresh())
	newer := connect(t, p, "newer@example.com", fresh())
	lists := map[string]map[string]bool{
		older: {"claude-opus-5": true},
		newer: {"claude-opus-5-5": true, "claude-opus-5": true},
	}
	v := Versions{
		Models: []string{"claude-opus-5-5", "claude-opus-5"},
		Lists:  func(id, model string) bool { return lists[id][model] },
	}
	fam := WithVersions(ctx, v)

	lease, err := p.Acquire(fam, "anthropic", "claude-opus-5-5", nil)
	if err != nil || lease.Account.ID != newer || lease.Model != "claude-opus-5-5" {
		t.Errorf("both can serve: %s on %s, %v; want claude-opus-5-5 on the newer plan, though it is second",
			lease.Model, lease.Account.Email, err)
	}

	// The newer account has failed this request: the older serves the
	// version it has, rather than the request being refused.
	lease, err = p.Acquire(fam, "anthropic", "claude-opus-5-5", map[string]bool{newer: true})
	if err != nil || lease.Account.ID != older || lease.Model != "claude-opus-5" {
		t.Errorf("newest's account tried: %s on %s, %v; want claude-opus-5 on the older plan",
			lease.Model, lease.Account.Email, err)
	}

	// A version switched off on an account is not its best: the newer one
	// falls back to the one before it.
	if err := st.SetAccountModel(ctx, newer, "claude-opus-5-5", false); err != nil {
		t.Fatal(err)
	}
	lease, err = p.Acquire(fam, "anthropic", "", nil)
	if err != nil || lease.Account.ID != older || lease.Model != "claude-opus-5" {
		t.Errorf("newest off: %s on %s, %v; want the first account's claude-opus-5", lease.Model, lease.Account.Email, err)
	}
	if !p.Offers(fam, "anthropic", "") {
		t.Error("Offers says no account serves the family")
	}

	// No account has any version on: refused as a model switched off.
	for _, id := range []string{older, newer} {
		if err := st.SetAccountModel(ctx, id, "claude-opus-5", false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Acquire(fam, "anthropic", "", nil); !errors.Is(err, ErrModelOff) {
		t.Errorf("no version anywhere: %v, want ErrModelOff", err)
	}
	if p.Offers(fam, "anthropic", "") {
		t.Error("Offers says a family no account has is served")
	}

	// A request with no versions is untouched: its own model, no lease model.
	lease, err = p.Acquire(ctx, "anthropic", "claude-haiku-4-5", nil)
	if err != nil || lease.Account.ID != older || lease.Model != "" {
		t.Errorf("a plain request: %q on %s, %v", lease.Model, lease.Account.Email, err)
	}
}

// Best is the newest version an account lists and has on, and a nil Lists
// leaves the switches alone to decide.
func TestBest(t *testing.T) {
	p, st := fixture(t)
	ctx := context.Background()
	id := connect(t, p, "a@example.com", fresh())
	if err := st.SetAccountModel(ctx, id, "gpt-6.1-sol", false); err != nil {
		t.Fatal(err)
	}
	a, err := st.Account(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	v := Versions{Models: []string{"gpt-6.1-sol", "gpt-6-sol", "gpt-5.6-sol"}}
	if got := Best(a, v); got != "gpt-6-sol" {
		t.Errorf("Best with the newest off = %q, want gpt-6-sol", got)
	}
	v.Lists = func(_, m string) bool { return m == "gpt-5.6-sol" }
	if got := Best(a, v); got != "gpt-5.6-sol" {
		t.Errorf("Best listing only the oldest = %q", got)
	}
	v.Lists = func(string, string) bool { return false }
	if got := Best(a, v); got != "" {
		t.Errorf("Best listing none = %q", got)
	}

	// A version failing on the account is passed over for the next, and is
	// served only when every version it has is failing: a family is tried,
	// never refused, for its versions failing.
	v.Lists = nil
	v.Failing = func(_, m string) bool { return m == "gpt-6-sol" }
	if got := Best(a, v); got != "gpt-5.6-sol" {
		t.Errorf("Best with its newest failing = %q, want gpt-5.6-sol", got)
	}
	v.Failing = func(string, string) bool { return true }
	if got := Best(a, v); got != "gpt-6-sol" {
		t.Errorf("Best with every version failing = %q, want the newest, gpt-6-sol", got)
	}
}
