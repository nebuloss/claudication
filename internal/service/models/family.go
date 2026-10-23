package models

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"claudication/internal/pool"
	"claudication/internal/service/clients"
	"claudication/internal/service/settings"
	"claudication/internal/store"
)

// Families: a model family is one choice — Claude Opus, GPT Sol — served by
// the newest of its versions the serving account has.
//
// The owner (2026-10-22): "per provider type, choose binding mode or
// per-model mode, so GPT and Claude can be on binding by default and reduce
// the number of models. My only concern is subscription levels (some plans
// may never allow the latest models)." So:
//
//  1. A family is resolved per account, at request time, never once for the
//     gateway: the versions the client may be served, newest first, go to the
//     pool (pool.Versions), which leases an account that has any of them —
//     the ones whose newest is the family's newest first, in the operator's
//     order, then the rest — and the request is sent the version that account
//     serves (relay.Rebind), its output budget fitted to that version's cap.
//     An account on a plan without the newest Opus serves Opus with the Opus
//     it has, and a request is refused only when no account has any.
//  2. Which version a family serves is decided once, for the gateway, on
//     Settings › Models (the owner, 2026-10-22), never per client name: each
//     family follows the latest — rule 1 — unless the operator picked one
//     version of it (Pinned), which every request for the family is then
//     sent, by the accounts that have it; an account without it does not
//     serve the family.
//  3. Each provider has a mode (Modes): families, the default, offers each
//     family as one choice everywhere a model is offered — a slot's menu, a
//     client's Allowed models, a direct-mode client's list and /v1/models, the
//     MCP server's list_models — and no version; every model offers each
//     version and no family, as the gateway did before families. The mode
//     changes what is offered and nothing that is bound: a binding made in
//     the other mode keeps serving, and its menu shows it as bound now.
//
// A family's facts, where a list or a config needs one window and one output
// limit, are its versions' smallest (withVirtual): a client sized for the
// newest would be refused by an account serving an older one with less room.
//
// Families are read off the accounts' own lists, never written down here: a
// Claude line is the word after claude- (claude-opus-5-5 is opus), a ChatGPT
// one the slug's last word (gpt-6.1-sol is sol; internal/service/clients,
// Follow, has the backend's own descriptions that make it the line), a
// Gemini one its tier (gemini-3-pro-preview is pro). A model
// whose id fits neither is no family's, and is offered as itself in either
// mode, so nothing an account lists is out of reach.

// familyPattern is each provider's family, from a model's id.
var familyPattern = map[string]*regexp.Regexp{
	"anthropic": regexp.MustCompile(`^claude-([a-z]+)-`),
	"openai":    regexp.MustCompile(`^gpt-\d+(?:\.\d+)*-([a-z]+)$`),
	// A tier, and a preview of it, are the family; a variant past the tier
	// (gemini-3.1-pro-preview-customtools) is a model of its own.
	"google": regexp.MustCompile(`^gemini-\d+(?:\.\d+)*-(pro|flash-lite|flash)(?:-preview)?$`),
}

// FamilyOf is the family a provider's model belongs to, or "".
func FamilyOf(provider, model string) clients.Follow {
	re := familyPattern[provider]
	if re == nil {
		return ""
	}
	m := re.FindStringSubmatch(strings.ToLower(model))
	if m == nil {
		return ""
	}
	return clients.FamilyFollow(provider, m[1])
}

// family is one family as the lists last said it.
type family struct {
	Follows clients.Follow
	// Versions are its models across every account's list, newest first.
	Versions []string
}

// listed entry, as the family ordering reads it.
type versioned struct {
	id, at   string
	version  []int
	priority int
	ranked   bool
	seen     int
}

var numbers = regexp.MustCompile(`\d+`)

// versionOf is the numbers in an id after its line's prefix: gpt-6.1-sol is
// [6 1], claude-opus-5-5 [5 5].
func versionOf(id string) []int {
	var out []int
	for _, n := range numbers.FindAllString(id, -1) {
		if v, err := strconv.Atoi(n); err == nil && len(n) < 8 { // a date suffix is no version
			out = append(out, v)
		}
	}
	return out
}

// newer reports whether a is the newer release: by date where both say one
// (Claude's lists do), else by version (ChatGPT's do not date: gpt-6.1-sol
// is newer than gpt-6-sol), else by the backend's ranking, else the list's
// order.
func newer(a, b versioned) bool {
	if a.at != "" && b.at != "" && a.at != b.at {
		// RFC 3339 times in one zone order as strings do.
		return a.at > b.at
	}
	for i := 0; i < len(a.version) || i < len(b.version); i++ {
		av, bv := -1, -1
		if i < len(a.version) {
			av = a.version[i]
		}
		if i < len(b.version) {
			bv = b.version[i]
		}
		if av != bv {
			return av > bv
		}
	}
	if a.ranked && b.ranked && a.priority != b.priority {
		return a.priority < b.priority
	}
	return a.seen < b.seen
}

// familiesOf groups a provider's listed models into families, each newest
// first, and orders the families: Claude's newest release first, as its lists
// date them; ChatGPT's by the backend's ranking of each one's newest, which is
// the order Codex's picker shows them in (sol, astra, luna, terra on
// 2026-10-19).
func familiesOf(providerID string, entries []versioned) []family {
	by := map[clients.Follow][]versioned{}
	var order []clients.Follow
	for _, e := range entries {
		f := FamilyOf(providerID, e.id)
		if f == "" {
			continue
		}
		if _, ok := by[f]; !ok {
			order = append(order, f)
		}
		by[f] = append(by[f], e)
	}
	for _, vs := range by {
		sort.SliceStable(vs, func(i, j int) bool { return newer(vs[i], vs[j]) })
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := by[order[i]][0], by[order[j]][0]
		if a.at != "" && b.at != "" && a.at != b.at {
			return a.at > b.at
		}
		if a.ranked && b.ranked && a.priority != b.priority {
			return a.priority < b.priority
		}
		return a.seen < b.seen
	})
	out := make([]family, 0, len(order))
	for _, f := range order {
		ids := make([]string, 0, len(by[f]))
		for _, v := range by[f] {
			ids = append(ids, v.id)
		}
		out = append(out, family{Follows: f, Versions: ids})
	}
	return out
}

// families are a provider's families as the lists last said them, newest
// first.
func (s *Catalog) families(ctx context.Context, providerID string) []family {
	s.refreshFamilies(ctx)
	s.rolesMu.Lock()
	defer s.rolesMu.Unlock()
	return append([]family(nil), s.fams[providerID]...)
}

// familyNamed is one family's versions as the lists last said them.
func (s *Catalog) familyNamed(ctx context.Context, f clients.Follow) (family, bool) {
	for _, fam := range s.families(ctx, f.Provider()) {
		if fam.Follows == f {
			return fam, true
		}
	}
	return family{}, false
}

// Lists reports whether an account's own model list names model, as it last
// said: what decides which version of a family the account serves
// (pool.Versions). An account whose list has not been read is taken to list
// it — the switches still decide, and its upstream says if it does not —
// rather than left out of a family it may well serve.
func (s *Catalog) Lists(accountID, model string) bool {
	s.rolesMu.Lock()
	defer s.rolesMu.Unlock()
	l, ok := s.accountLists[accountID]
	return !ok || l[store.ModelKey(model)]
}

// versionsFor is what the pool is handed for a family a client may be served:
// its versions the client is allowed, newest first, and this catalogue's
// reading of who lists what.
func (s *Catalog) versionsFor(allowed []string) pool.Versions {
	return pool.Versions{Models: allowed, Lists: s.Lists}
}

// resolvedFamily is a family as one client may be served it now.
type resolvedFamily struct {
	// allowed are its versions the client may be served, newest first:
	// what a request hands the pool.
	allowed []string
	// serving are the versions some account would serve it with now, newest
	// first: the first is what a request gets where an account has it, and
	// all of them are what its facts are the smallest of.
	serving []string
}

// resolveFamily is how a family serves a client now, and why it cannot when
// it cannot.
func (s *Catalog) resolveFamily(ctx context.Context, off Off, f clients.Follow) (resolvedFamily, string) {
	var r resolvedFamily
	if !off.allows(f.ModelID()) {
		return r, whyNotAllowed
	}
	fam, ok := s.familyNamed(ctx, f)
	if !ok {
		return r, "no account lists a " + f.Title() + " model"
	}
	versions := fam.Versions
	if p := s.Pinned(f); p != "" {
		// One version for the gateway: only it, and only where listed.
		versions = nil
		for _, v := range fam.Versions {
			if store.ModelKey(v) == store.ModelKey(p) {
				versions = []string{v}
			}
		}
		if versions == nil {
			return r, f.Title() + " is set to " + p + " on Settings › Models, which no connected account lists"
		}
	}
	for _, v := range versions {
		if off.allows(v) {
			r.allowed = append(r.allowed, v)
		}
	}
	if len(r.allowed) == 0 {
		return r, whyNotAllowed
	}
	candidates, err := s.pool.Candidates(ctx, f.Provider())
	if err != nil {
		return r, whyNoAccount
	}
	v := s.versionsFor(r.allowed)
	seen := map[string]bool{}
	for _, a := range candidates {
		if b := pool.Best(a, v); b != "" {
			seen[b] = true
		}
	}
	for _, m := range r.allowed {
		if seen[m] {
			r.serving = append(r.serving, m)
		}
	}
	if len(r.serving) == 0 {
		return r, s.familyUnavailable(ctx, f.Provider(), r.allowed)
	}
	return r, ""
}

// familyUnavailable is why no account serves any of a family's versions: as
// for one model (unavailable), but per account — a version one account has
// on is no use from another that does not list it.
func (s *Catalog) familyUnavailable(ctx context.Context, provider string, versions []string) string {
	accounts, err := s.store.ListAccounts(ctx)
	if err != nil {
		return whyNoAccount
	}
	listed, on := false, false
	for _, a := range accounts {
		if a.Provider != provider || a.Disabled() {
			continue
		}
		for _, v := range versions {
			if s.Lists(a.ID, v) {
				listed = true
				on = on || a.Serves(v)
			}
		}
	}
	switch {
	case !listed:
		return whyNotListed
	case !on:
		return whyOff
	}
	return whyNoAccount
}

// PinSetting is the settings row holding the one version a family serves,
// "" (or no row) for the latest.
func PinSetting(f clients.Follow) string { return "models.version." + string(f) }

// LoadPins reads every family's version from the settings, once at start;
// SetPin keeps them after. A failure leaves every family on the latest,
// which is what each was before there was a choice.
func (s *Catalog) LoadPins(ctx context.Context) error {
	all, err := s.settings.Settings(ctx)
	if err != nil {
		return err
	}
	pins := map[clients.Follow]string{}
	for k, v := range all {
		if f, ok := strings.CutPrefix(k, "models.version."); ok && v != "" && clients.Follow(f).Valid() {
			pins[clients.Follow(f)] = v
		}
	}
	s.pinMu.Lock()
	s.pins = pins
	s.pinMu.Unlock()
	return nil
}

// Pinned is the one version a family serves, "" while it follows the latest.
func (s *Catalog) Pinned(f clients.Follow) string {
	s.pinMu.Lock()
	defer s.pinMu.Unlock()
	return s.pins[f]
}

// SetPin sets the version a family serves for the whole gateway: one the
// connected accounts list, or "" for the latest, per account. The store is
// written first, as a switch is, so a choice that took effect but would not
// survive a restart is not a state this can reach.
func (s *Catalog) SetPin(ctx context.Context, f clients.Follow, model string) error {
	if !f.Valid() {
		return fmt.Errorf("no family %s", f)
	}
	model = strings.TrimSpace(model)
	if model != "" {
		fam, ok := s.familyNamed(ctx, f)
		found := false
		for _, v := range fam.Versions {
			found = found || store.ModelKey(v) == store.ModelKey(model)
		}
		if !ok || !found {
			return fmt.Errorf("%w: no connected account lists %s as a %s model", ErrNotAVersion, model, f.Title())
		}
	}
	if err := s.settings.SetSetting(ctx, PinSetting(f), model); err != nil {
		return err
	}
	s.pinMu.Lock()
	defer s.pinMu.Unlock()
	if s.pins == nil {
		s.pins = map[clients.Follow]string{}
	}
	if model == "" {
		delete(s.pins, f)
	} else {
		s.pins[f] = model
	}
	return nil
}

// ErrNotAVersion is a version asked of a family that none of the accounts
// list as one of it.
var ErrNotAVersion = errors.New("not a version of the family")

// ModeSetting is the settings row holding whether a provider offers its
// models by family.
func ModeSetting(provider string) string { return "models." + provider + ".families" }

// Modes are each provider's families switch: on, its models are offered by
// family; off, every model. On unless switched off — the owner's default for
// both providers — and so a switch that cannot be read leaves them on.
func (s *Catalog) Modes() map[string]*settings.Switch { return s.modes }

// ByFamily reports whether a provider offers its models by family.
func (s *Catalog) ByFamily(provider string) bool {
	sw := s.modes[provider]
	return sw == nil || sw.On()
}

// FamilyReport is one provider's families as the Models settings page shows
// them: its mode, each family with its versions, and which version of each
// family every account would serve.
//
// Only what connected accounts list, the owner's rule (2026-10-22): never
// the public catalogue, never a model no switched-on account lists. A version
// every such account has switched off is kept, marked Off, so it can be found
// and switched back on from an account; a family is kept while one of them
// lists any version of it.
type FamilyReport struct {
	Provider string `json:"provider"`
	Title    string `json:"title"`
	// Connected is whether an account of the provider is switched on: with
	// none, there is nothing to list, and the mode applies once one is.
	Connected bool `json:"connected"`
	ByFamily  bool `json:"by_family"`
	// ModeStored is whether an operator set the mode, as opposed to its
	// default.
	ModeStored bool           `json:"mode_stored"`
	Families   []FamilyRow    `json:"families"`
	Accounts   []AccountServe `json:"accounts"`
	// Other are the models no family takes, offered as themselves.
	Other []string `json:"other,omitempty"`
}

// FamilyRow is one family.
type FamilyRow struct {
	Follows clients.Follow `json:"follows"`
	// ID is what a direct-mode client names it by: claude-opus.
	ID    string `json:"id"`
	Title string `json:"title"`
	// Serving is the version a request gets where an account has it — the
	// newest any account would serve — "" when none would.
	Serving string `json:"serving"`
	// Pinned is the one version the operator set the family to serve, ""
	// while it follows the latest.
	Pinned string `json:"pinned,omitempty"`
	// Why is why no account serves it, when none does.
	Why      string       `json:"why,omitempty"`
	Versions []VersionRow `json:"versions"`
}

// VersionRow is one version of a family.
type VersionRow struct {
	Model string `json:"model"`
	Title string `json:"title,omitempty"`
	// Listed are the switched-on accounts whose list names it, and ServedBy
	// those that would serve the family with it: their newest of it, on.
	Listed   []string `json:"listed"`
	ServedBy []string `json:"served_by"`
	// On are those of Listed that have it on: who could serve it, were the
	// family set to it.
	On []string `json:"on"`
	// Off is a version every account listing it has switched off.
	Off bool `json:"off,omitempty"`
}

// AccountServe is which version of each family one account would serve.
type AccountServe struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Disabled bool   `json:"disabled"`
	// Serves is, by family, the version it would serve: "" for a family it
	// has no version of on.
	Serves map[clients.Follow]string `json:"serves"`
}

// listedBy reports whether an account's own list, as last read, names
// model: only a list actually read, unlike Lists, since what is shown here
// as an account's must be what it said.
func (s *Catalog) listedBy(accountID, model string) bool {
	s.rolesMu.Lock()
	defer s.rolesMu.Unlock()
	return s.accountLists[accountID][store.ModelKey(model)]
}

// Report is every provider's families, for the Models settings page and an
// account's own page. Read for no client, so nothing is left out for being
// switched off on one. An account switched off lists nothing here, as it
// serves nothing, and still says what it would serve, marked.
func (s *Catalog) Report(ctx context.Context) ([]FamilyReport, error) {
	s.refreshFamilies(ctx)
	accounts, err := s.store.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	var out []FamilyReport
	for _, r := range s.relays {
		p := r.Wire.ID()
		rep := FamilyReport{Provider: p, Title: clients.ProviderTitle(p), ByFamily: s.ByFamily(p),
			Families: []FamilyRow{}, Accounts: []AccountServe{}}
		if sw := s.modes[p]; sw != nil {
			rep.ModeStored = sw.Stored()
		}
		var mine, on []store.Account
		for _, a := range accounts {
			if a.Provider != p {
				continue
			}
			mine = append(mine, a)
			rep.Accounts = append(rep.Accounts, AccountServe{ID: a.ID, Name: a.DisplayName(), Disabled: a.Disabled(),
				Serves: map[clients.Follow]string{}})
			if !a.Disabled() {
				on = append(on, a)
			}
		}
		rep.Connected = len(on) > 0
		if !rep.Connected {
			out = append(out, rep)
			continue
		}
		names := map[string]string{}
		s.rolesMu.Lock()
		listed := append([]listedModel(nil), s.listed[p]...)
		s.rolesMu.Unlock()
		for _, m := range listed {
			names[store.ModelKey(m.ID)] = m.Name
			if FamilyOf(p, m.ID) == "" {
				rep.Other = append(rep.Other, m.ID)
			}
		}
		for _, fam := range s.families(ctx, p) {
			row := FamilyRow{Follows: fam.Follows, ID: fam.Follows.ModelID(), Title: fam.Follows.Title(),
				Pinned: s.Pinned(fam.Follows), Versions: []VersionRow{}}
			res, why := s.resolveFamily(ctx, nil, fam.Follows)
			v := s.versionsFor(res.allowed)
			best := map[string]string{}
			for i, a := range mine {
				b := pool.Best(a, v)
				best[a.ID] = b
				rep.Accounts[i].Serves[fam.Follows] = b
			}
			if why == "" {
				row.Serving = res.serving[0]
			} else {
				row.Why = why
			}
			for _, m := range fam.Versions {
				vr := VersionRow{Model: m, Title: names[store.ModelKey(m)], Listed: []string{}, ServedBy: []string{},
					On: []string{}}
				anyOn := false
				for _, a := range on {
					if !s.listedBy(a.ID, m) {
						continue
					}
					vr.Listed = append(vr.Listed, a.ID)
					if a.Serves(m) {
						anyOn = true
						vr.On = append(vr.On, a.ID)
					}
					if best[a.ID] == m {
						vr.ServedBy = append(vr.ServedBy, a.ID)
					}
				}
				if len(vr.Listed) == 0 {
					// Listed once by an account since switched off or gone.
					continue
				}
				vr.Off = !anyOn
				row.Versions = append(row.Versions, vr)
			}
			if len(row.Versions) > 0 {
				rep.Families = append(rep.Families, row)
			}
		}
		out = append(out, rep)
	}
	return out, nil
}
