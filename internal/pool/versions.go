package pool

import (
	"context"
	"sort"

	"claudication/internal/store"
)

// Versions is a request any of several versions of one model can serve: a
// family (claude:opus, openai:sol; internal/service/clients, Follow), each
// account serving the newest of them it has.
//
// Why per account and not one newest for the whole gateway: subscriptions
// differ in what they list. A plan without the newest Opus lists the one
// before it, and a request for Opus that such an account takes is served by
// that one, rather than sent the newest — which its upstream refuses — or
// refused because another account has a newer one. The owner's concern,
// 2026-10-22: some plans may never allow the latest models.
type Versions struct {
	// Models are the versions the client may be served, newest first; which
	// of them an account has is Best's to read.
	Models []string
	// Lists reports whether an account's own model list names a model. Nil
	// takes every account to list everything, which leaves the switches alone
	// to decide.
	Lists func(accountID, model string) bool
	// Failing reports whether a model is failing on an account now
	// (internal/service/availability). Nil takes none to be.
	Failing func(accountID, model string) bool
}

// Best is the version an account would serve: the newest of v's models it
// lists and has on and that is not failing there — so a family whose newest
// version stopped answering on an account is served the one before it —
// else the newest it lists and has on, failing or not, so a family is never
// refused for its versions failing: it is tried, and the upstream says.
// "" when it has none of them.
func Best(a store.Account, v Versions) string {
	fallback := ""
	for _, m := range v.Models {
		if !a.Serves(m) || (v.Lists != nil && !v.Lists(a.ID, m)) {
			continue
		}
		if v.Failing == nil || !v.Failing(a.ID, m) {
			return m
		}
		if fallback == "" {
			fallback = m
		}
	}
	return fallback
}

type versionsKey struct{}

// WithVersions marks a request as one any of v's models can serve: Acquire
// then leases an account that has any of them, preferring the accounts whose
// best is the newest, and says on the lease which one that account serves.
func WithVersions(ctx context.Context, v Versions) context.Context {
	return context.WithValue(ctx, versionsKey{}, v)
}

func versionsOf(ctx context.Context) (Versions, bool) {
	v, ok := ctx.Value(versionsKey{}).(Versions)
	return v, ok && len(v.Models) > 0
}

// byVersion is the candidates that have any of v's models, the accounts whose
// best is the newest first and the operator's order kept within each: so the
// newest is served wherever an account has it with room, and an older one is
// served rather than refused when none does — pick walks this order, its
// passes over quota and cooling unchanged. Each account's best rides with it.
func byVersion(candidates []store.Account, v Versions) ([]store.Account, map[string]string) {
	rank := make(map[string]int, len(v.Models))
	for i, m := range v.Models {
		rank[m] = i
	}
	best := map[string]string{}
	out := make([]store.Account, 0, len(candidates))
	for _, a := range candidates {
		if m := Best(a, v); m != "" {
			best[a.ID] = m
			out = append(out, a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return rank[best[out[i].ID]] < rank[best[out[j].ID]] })
	return out, best
}

// Candidates are a provider's accounts that could take a request now, in the
// operator's order, cooling and quota aside: what Acquire picks from, read by
// the model list to say which version of a family each would serve.
func (p *Pool) Candidates(ctx context.Context, provider string) ([]store.Account, error) {
	accounts, err := p.store.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	return candidatesFor(accounts, provider, "", nil, p.now()), nil
}
