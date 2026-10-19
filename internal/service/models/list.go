// Package models is the gateway's model list: what it serves, under which
// names, with which facts, and which upstream serves each — one answer for
// /v1/models, the admin screens, the configs the admin UI writes, the public
// docs page and the routing of every request.
//
// It owns the four things that answer is made of: the upstream lists, asked
// of each account of each provider (list.go); the public catalogue those
// lists are matched against (catalog.go); the aliases — the roles, the
// newest model of each Claude family, and the operator's own; and the route
// a requested name takes (route.go).
//
// A service of its own because three surfaces read it and none of them owns
// it. It lived in the gateway, which handed it to the admin API as five
// separate functions and to the docs page as a closure; each surface now takes
// the narrow part of it that it reads.
package models

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"claudication/internal/config"
	"claudication/internal/pool"
	"claudication/internal/provider"
	"claudication/internal/relay"
	"claudication/internal/store"
)

// Store is what the catalogue reads: the accounts it asks, and the aliases
// and bindings the operator stored.
type Store interface {
	ListAccounts(ctx context.Context) ([]store.Account, error)
	Account(ctx context.Context, id string) (store.Account, error)
	CustomAliases(ctx context.Context) ([]string, error)
	RoleBinding(ctx context.Context, keyID, client, role string) (model, by string, err error)
}

// Pool is what the catalogue asks of the account pool: a token to read an
// account's list with, and whether any account serves a model now.
type Pool interface {
	AccessToken(ctx context.Context, id string) (string, error)
	Offers(ctx context.Context, provider, model string) bool
}

// Catalog is the model list and the aliases over it.
type Catalog struct {
	cfg        *config.Config
	log        *slog.Logger
	store      Store
	pool       Pool
	relays     []*relay.Relay
	httpClient *http.Client

	// What the accounts' lists last said, for the aliases and the routes;
	// see remember.
	rolesMu sync.Mutex
	// newest is the newest model of each Claude family, and families the
	// families newest release first, for the claude-* aliases.
	newest   map[string]string
	families []string
	// ranked is the ChatGPT backend's models in its own ranking, for
	// gpt-large and gpt-small.
	ranked []string
	// served is which provider's lists name each model, by store.ModelKey:
	// the route a real model takes. connected are the providers with an
	// account switched on, and unread those of them none of whose lists
	// could be read.
	served    map[string]map[string]bool
	connected map[string]bool
	unread    map[string]bool
	newestAt  time.Time

	// The public model catalogue; see catalog.go.
	catalog catalogCache
}

// Deps is what a Catalog is built from. Every field is required.
type Deps struct {
	// Config holds the catalogue's address, read on each refresh.
	Config *config.Config
	Log    *slog.Logger
	Store  Store
	Pool   Pool
	// Relays are the providers, one relay each, the first the gateway's
	// default: whose wire an account's list is read over, so each
	// provider's address, headers and list shape stay in its own package.
	Relays []*relay.Relay
	// HTTPClient makes the small upstream calls: the lists and the catalogue.
	HTTPClient *http.Client
}

// New builds a Catalog. Nothing is read until it is asked.
func New(d Deps) *Catalog {
	return &Catalog{
		cfg: d.Config, log: d.Log, store: d.Store, pool: d.Pool,
		relays: d.Relays, httpClient: d.HTTPClient,
	}
}

// relayFor is a provider's relay, or nil for one the gateway does not serve.
func (s *Catalog) relayFor(providerID string) *relay.Relay {
	for _, r := range s.relays {
		if r.Wire.ID() == providerID {
			return r
		}
	}
	return nil
}

// defaultProvider is the provider a name no list knows is sent to: the
// first, Anthropic, where every request went before there was a second.
func (s *Catalog) defaultProvider() string {
	if len(s.relays) == 0 {
		return ""
	}
	return s.relays[0].Wire.ID()
}

// Public is the list as the public docs page shows it: as a key that binds no
// alias and is kept off nothing sees it, so the configs written from it ask
// for the roles as every other config does.
func (s *Catalog) Public(ctx context.Context) ([]byte, error) {
	return s.For(ctx, store.APIKey{}, "")
}

// For is the model list one key is offered from one client: the aliases
// first, each carrying the facts of the model it serves for that key and
// client, then the real models, less what the key may not use. The
// /v1/models answer, and what the admin UI writes that key's configs from, so
// the two cannot disagree. client is as api.ClientName names it; empty is a
// caller that is no client in particular.
//
// Every provider's models are in it; a ChatGPT one carries "provider":
// "openai", and so does an alias serving one, so a surface that cannot reach
// a provider can leave its entries out (the gateway's /v1/models does).
//
// An alias is listed only while it can serve.
func (s *Catalog) For(ctx context.Context, key store.APIKey, client string) ([]byte, error) {
	body, fromCatalog, err := s.list(ctx)
	if err != nil {
		return nil, err
	}
	aliases, err := s.Aliases(ctx)
	if err != nil {
		return nil, err
	}
	var entries []virtualEntry
	for _, a := range aliases {
		// Listed from the catalogue, no Claude account can serve anything
		// yet, and a Claude alias is shown as it will serve once one is
		// connected — the configs written now are the ones that will be
		// used then.
		target, why := s.RoleTarget(ctx, key, client, a.Name)
		if fromCatalog && target != "" && key.Allows(target) && s.ProviderOf(ctx, target) == s.defaultProvider() {
			why = ""
		}
		if why != "" {
			continue
		}
		entries = append(entries, virtualEntry{
			ID: a.Name, DisplayName: roleLabel(a),
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
			Serving:   target, Role: strings.TrimPrefix(a.Name, "claude-"),
		})
	}
	if len(entries) > 0 {
		body = withVirtual(body, entries)
	}
	// Less what the key may not use, for the reason the switched-off models
	// are left out: a client builds its menu from this, and a model on the
	// menu that is refused when picked is worse than one not offered.
	if len(key.ModelsOff) > 0 {
		body = modelsAllowedFor(body, key)
	}
	return body, nil
}

// virtualEntry is a virtual model as a model list shows it. The entry is the
// serving model's own, with the id and name changed — so its context window,
// output limit and capabilities are the real ones, and a client sizing its
// requests from the list sizes them right — plus two fields of the gateway's,
// which clients ignore and the admin UI and config writer read.
type virtualEntry struct {
	ID          string
	DisplayName string
	CreatedAt   string
	Serving     string
	// Role is the alias without its claude- prefix — large, small, opus,
	// sonnet, haiku, fable, and gpt-large and gpt-small as they are — for the
	// config writer.
	Role string
}

// roleLabel is how an alias reads in a client's model picker.
func roleLabel(a store.Alias) string {
	name := strings.TrimPrefix(a.Name, "claude-")
	switch {
	case a.Name == store.RoleGPTLarge || a.Name == store.RoleGPTSmall:
		return "GPT (" + strings.TrimPrefix(a.Name, "gpt-") + ")"
	case store.IsRoleName(a.Name):
		return "Claude (" + name + ")"
	case a.Builtin:
		return "Claude " + strings.ToUpper(name[:1]) + name[1:]
	}
	return a.Name
}

// newestTTL is how long the families, the ranking and who serves what are
// remembered. The lists come from the upstreams, once per account, and a
// model is released every few months; a request should not wait on them.
const newestTTL = 10 * time.Minute

// familyOf is a real model's family, from its id: claude-opus-5-5 is opus.
var familyOf = regexp.MustCompile(`^claude-([a-z]+)-`)

// remembered is what one read of every provider's list found, for remember
// to keep.
type remembered struct {
	newest    map[string]string
	families  []string
	ranked    []string
	served    map[string]map[string]bool
	connected map[string]bool
	unread    map[string]bool
}

// rememberProvider notes what one provider's list says: which models it
// serves, and, for the providers whose aliases follow it, the newest of each
// Claude family or the ChatGPT ranking.
func (m *remembered) rememberProvider(providerID string, list []byte) {
	var l struct {
		Data []struct {
			ID        string `json:"id"`
			CreatedAt string `json:"created_at"`
			Virtual   bool   `json:"virtual"`
			Priority  *int   `json:"priority"`
		} `json:"data"`
	}
	if json.Unmarshal(list, &l) != nil {
		return
	}
	if m.served == nil {
		m.served = map[string]map[string]bool{}
	}
	ids := map[string]bool{}
	for _, e := range l.Data {
		if !e.Virtual && e.ID != "" {
			ids[store.ModelKey(e.ID)] = true
		}
	}
	m.served[providerID] = ids

	if store.AliasProvider(store.RoleGPTLarge) == providerID {
		// By the priority each entry carries, not the list's order, which
		// the catalogue may have changed; unranked ones after, as listed.
		type rank struct {
			id       string
			priority int
			known    bool
		}
		var rs []rank
		for _, e := range l.Data {
			if e.Virtual || e.ID == "" {
				continue
			}
			r := rank{id: e.ID}
			if e.Priority != nil {
				r.priority, r.known = *e.Priority, true
			}
			rs = append(rs, r)
		}
		sort.SliceStable(rs, func(i, j int) bool {
			if rs[i].known != rs[j].known {
				return rs[i].known
			}
			return rs[i].priority < rs[j].priority
		})
		for _, r := range rs {
			m.ranked = append(m.ranked, r.id)
		}
		return
	}
	if store.AliasProvider(store.RoleLarge) != providerID {
		return
	}
	// The newest model of each family, by release date — not by the list's
	// order, and not from a list of families written down here: a family
	// Anthropic adds is found the day an account lists it. Families are kept
	// newest release first.
	type pick struct {
		id, at string
		seen   int // where the family first appears, for ties
	}
	newest := map[string]pick{}
	for i, e := range l.Data {
		match := familyOf.FindStringSubmatch(e.ID)
		if e.Virtual || match == nil {
			continue
		}
		// RFC 3339 times in one zone order as strings do. A later entry
		// wins only when strictly newer, so a list that already runs newest
		// first keeps its first.
		have, ok := newest[match[1]]
		if !ok {
			newest[match[1]] = pick{e.ID, e.CreatedAt, i}
		} else if e.CreatedAt > have.at {
			newest[match[1]] = pick{e.ID, e.CreatedAt, have.seen}
		}
	}
	families := make([]string, 0, len(newest))
	byFamily := make(map[string]string, len(newest))
	for f, p := range newest {
		families = append(families, f)
		byFamily[f] = p.id
	}
	sort.Slice(families, func(i, j int) bool {
		a, b := newest[families[i]].at, newest[families[j]].at
		if a != b {
			return a > b
		}
		// Undated, or released together: the accounts' own order, newest first.
		return newest[families[i]].seen < newest[families[j]].seen
	})
	m.newest, m.families = byFamily, families
}

// remember keeps what a read of the lists found. A read that found no list at
// all keeps what the last one did, and only says which providers could not be
// read — what ProviderOf guesses for — and is not counted as fresh, so the
// next caller tries again, as one always has.
func (s *Catalog) remember(m remembered, read bool) {
	s.rolesMu.Lock()
	defer s.rolesMu.Unlock()
	s.unread, s.connected = m.unread, m.connected
	if !read {
		return
	}
	// A provider that could not be read this time keeps what it last said,
	// so one upstream's bad minute does not empty its aliases.
	for p, unread := range m.unread {
		if !unread {
			continue
		}
		if old, ok := s.served[p]; ok {
			if m.served == nil {
				m.served = map[string]map[string]bool{}
			}
			m.served[p] = old
		}
		if p == store.AliasProvider(store.RoleGPTLarge) && len(m.ranked) == 0 {
			m.ranked = s.ranked
		}
		if p == store.AliasProvider(store.RoleLarge) && m.newest == nil {
			m.newest, m.families = s.newest, s.families
		}
	}
	s.newest, s.families, s.ranked, s.served = m.newest, m.families, m.ranked, m.served
	s.newestAt = time.Now()
}

// refreshFamilies re-reads the model lists when what was remembered is stale.
func (s *Catalog) refreshFamilies(ctx context.Context) {
	s.rolesMu.Lock()
	stale := time.Since(s.newestAt) >= newestTTL
	s.rolesMu.Unlock()
	if !stale {
		return
	}
	_, _ = s.List(ctx)
}

// newestOf is the newest model of a family.
func (s *Catalog) newestOf(ctx context.Context, family string) string {
	s.refreshFamilies(ctx)
	s.rolesMu.Lock()
	defer s.rolesMu.Unlock()
	return s.newest[family]
}

// generation is a GPT model's major version, from its id: gpt-6.1-sol is 6.
var generation = regexp.MustCompile(`^gpt-(\d+)`)

// gptDefault is what an unbound GPT role follows, from the backend's own
// ranking of the models a ChatGPT account lists (the priority each entry
// carries, which is the order Codex's model picker shows them in):
//
//   - gpt-large is the first-ranked: the backend's own first choice.
//     2026-10-19: gpt-6.1-sol, "Latest workhorse model for coding and
//     everyday work", priority 1.
//   - gpt-small is the last-ranked model of the first-ranked one's
//     generation — the run of models at the top of the ranking sharing its
//     major version, and the last of them. The backend ranks its smaller
//     models of a generation below its larger ones and the previous
//     generation below both, so this is the current generation's light
//     model rather than an old one. 2026-10-19: gpt-6-luna, "Fast and
//     affordable model for easier tasks", priority 5 — the same model the
//     backend itself named, in x-codex-safety-buffering-faster-model, as its
//     faster one.
//
// When the first-ranked id carries no version to compare, small is the
// last-ranked model of all.
func (s *Catalog) gptDefault(ctx context.Context, role string) string {
	s.refreshFamilies(ctx)
	s.rolesMu.Lock()
	ranked := append([]string(nil), s.ranked...)
	s.rolesMu.Unlock()
	if len(ranked) == 0 {
		return ""
	}
	if role == store.RoleGPTLarge {
		return ranked[0]
	}
	top := generation.FindStringSubmatch(ranked[0])
	if top == nil {
		return ranked[len(ranked)-1]
	}
	small := ranked[0]
	for _, id := range ranked[1:] {
		g := generation.FindStringSubmatch(id)
		if g == nil || g[1] != top[1] {
			break
		}
		small = id
	}
	return small
}

// Aliases is every alias the gateway answers to now: the roles, one per
// Claude family the accounts list (newest release first), and the
// operator's own.
func (s *Catalog) Aliases(ctx context.Context) ([]store.Alias, error) {
	s.refreshFamilies(ctx)
	out := []store.Alias{
		{Name: store.RoleLarge, Family: store.AliasFamily(store.RoleLarge), Builtin: true},
		{Name: store.RoleSmall, Family: store.AliasFamily(store.RoleSmall), Builtin: true},
		{Name: store.RoleGPTLarge, Builtin: true},
		{Name: store.RoleGPTSmall, Builtin: true},
	}
	s.rolesMu.Lock()
	for _, f := range s.families {
		out = append(out, store.Alias{Name: "claude-" + f, Family: f, Builtin: true})
	}
	s.rolesMu.Unlock()
	custom, err := s.store.CustomAliases(ctx)
	if err != nil {
		return nil, err
	}
	for _, name := range custom {
		out = append(out, store.Alias{Name: name})
	}
	return out, nil
}

// IsAlias reports whether a request's model is one of the aliases. A real id
// always carries a version, so only a version-less claude-<family> has to be
// checked against the families, and the model list is not read for a
// request naming a real model.
func (s *Catalog) IsAlias(ctx context.Context, model string) bool {
	if store.IsRoleName(model) {
		return true
	}
	if family := store.AliasFamily(model); family != "" {
		return s.newestOf(ctx, family) != ""
	}
	custom, err := s.store.CustomAliases(ctx)
	if err != nil {
		return false
	}
	key := store.ModelKey(model)
	for _, name := range custom {
		if name == key {
			return true
		}
	}
	return false
}

// RoleTarget is the real model an alias serves for a key and a client: the
// key's binding, else the client's, else the gateway's, else — for a
// built-in — what its provider's list says it follows. why is non-empty when
// it cannot serve — the key may not use it, or no account has it on — and
// says so, naming the model and the binding that chose it, which is the one
// to change.
func (s *Catalog) RoleTarget(ctx context.Context, key store.APIKey, client, role string) (target, why string) {
	role = store.ModelKey(role)
	bound, by, err := s.store.RoleBinding(ctx, key.ID, client, role)
	if err != nil {
		return "", "could not read the model aliases"
	}
	target = bound
	// What chose the target, for the refusal: an operator who moved Codex's
	// claude-large and sees "no connected account has it on" in Codex has to
	// know it was Codex's binding and not the gateway's.
	var chose string
	switch by {
	case store.BoundByKey:
		chose = " for this API key"
	case store.BoundByClient:
		chose = " for " + client
	case store.BoundByGateway:
		chose = " on the gateway"
	}
	if target == "" {
		switch family := store.AliasFamily(role); {
		case family != "":
			target = s.newestOf(ctx, family)
			if target == "" {
				return "", role + " follows the newest " + family + " model, and no account lists one"
			}
		case store.AliasProvider(role) != "":
			target = s.gptDefault(ctx, role)
			if target == "" {
				return "", role + " follows the ChatGPT backend's ranking, and no ChatGPT account lists a model"
			}
		default:
			return "", role + " points to no model"
		}
	}
	if !key.Allows(target) {
		return target, role + " is " + target + chose + ", which this API key may not use"
	}
	if !s.pool.Offers(ctx, s.ProviderOf(ctx, target), target) {
		return target, role + " is " + target + chose + ", which no connected account has on"
	}
	return target, ""
}

// withVirtual puts the virtual models at the top of a model list, dropping a
// real entry whose id one of them takes over. A list it cannot read is
// returned as it was.
func withVirtual(body []byte, entries []virtualEntry) []byte {
	var list map[string]json.RawMessage
	if err := json.Unmarshal(body, &list); err != nil {
		return body
	}
	var data []json.RawMessage
	if err := json.Unmarshal(list["data"], &data); err != nil {
		return body
	}
	real := map[string]json.RawMessage{}
	for _, m := range data {
		var model struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(m, &model) == nil {
			real[store.ModelKey(model.ID)] = m
		}
	}
	taken := map[string]bool{}
	merged := make([]json.RawMessage, 0, len(entries)+len(data))
	for _, e := range entries {
		fields := map[string]any{"type": "model"}
		if base, ok := real[store.ModelKey(e.Serving)]; ok {
			_ = json.Unmarshal(base, &fields)
		}
		fields["id"] = e.ID
		fields["display_name"] = e.DisplayName
		fields["created_at"] = e.CreatedAt
		fields["virtual"] = true
		fields["serving"] = e.Serving
		fields["role"] = e.Role
		raw, err := json.Marshal(fields)
		if err != nil {
			return body
		}
		taken[store.ModelKey(e.ID)] = true
		merged = append(merged, raw)
	}
	for _, m := range data {
		var model struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(m, &model) == nil && taken[store.ModelKey(model.ID)] {
			continue
		}
		merged = append(merged, m)
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return body
	}
	list["data"] = encoded
	// The page now starts with a virtual model; first_id has to say so for a
	// client that pages by it.
	if _, ok := list["first_id"]; ok {
		if first, err := json.Marshal(entries[0].ID); err == nil {
			list["first_id"] = first
		}
	}
	out, err := json.Marshal(list)
	if err != nil {
		return body
	}
	return out
}

// modelsAllowedFor drops from a model list the models a key is kept off. A
// list it cannot read is returned as it was: the request still enforces.
func modelsAllowedFor(body []byte, key store.APIKey) []byte {
	var list map[string]json.RawMessage
	if err := json.Unmarshal(body, &list); err != nil {
		return body
	}
	var data []json.RawMessage
	if err := json.Unmarshal(list["data"], &data); err != nil {
		return body
	}
	kept := make([]json.RawMessage, 0, len(data))
	for _, m := range data {
		var model struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(m, &model) == nil && !key.Allows(model.ID) {
			continue
		}
		kept = append(kept, m)
	}
	encoded, err := json.Marshal(kept)
	if err != nil {
		return body
	}
	list["data"] = encoded
	out, err := json.Marshal(list)
	if err != nil {
		return body
	}
	return out
}

// List is the models this gateway serves: one answer for /v1/models,
// the admin UI and the configs it writes, and the public docs page alike.
//
// The union of what each account able to serve lists, less what is switched
// off on that account, every provider's in turn. Per account, because
// subscriptions differ: a model only the second account's plan has is still a
// model the gateway serves, and asking only the first would leave it out. And
// less the switches, because a model off everywhere is one a client would
// only meet as a refusal; clients that build their menus from this list —
// Claude Code, and the configs the admin UI writes from it — leave it out
// instead.
//
// One upstream call per account, in priority order, each for its whole list:
// a client's paging asked of every account separately would page nothing it
// could follow, so the answer is always all of it. An account whose list cannot
// be read is skipped; only when none can is it an error.
func (s *Catalog) List(ctx context.Context) ([]byte, error) {
	body, _, err := s.list(ctx)
	return body, err
}

// list is List, saying whether the default provider's part of it is the
// catalogue's because none of its accounts' lists could be read.
func (s *Catalog) list(ctx context.Context) (body []byte, fromCatalog bool, err error) {
	accounts, err := s.store.ListAccounts(ctx)
	if err != nil {
		return nil, false, err
	}
	var (
		parts   [][]byte
		lastErr error
		seen    remembered
	)
	for _, r := range s.relays {
		providerID := r.Wire.ID()
		var (
			lists   [][]byte
			serving []store.Account
		)
		for _, a := range accounts {
			if a.Provider != providerID || a.Disabled() {
				continue
			}
			body, err := s.ListFor(ctx, a.ID, "limit=1000")
			if err != nil {
				lastErr = err
				continue
			}
			lists = append(lists, body)
			serving = append(serving, a)
		}
		if hasAccounts(accounts, providerID) {
			if seen.connected == nil {
				seen.connected, seen.unread = map[string]bool{}, map[string]bool{}
			}
			seen.connected[providerID] = true
			seen.unread[providerID] = len(lists) == 0
		}
		var part []byte
		switch {
		case len(lists) > 0:
			// Matched again over the union: each account's list was matched
			// on its own, and the union runs first account first.
			part = s.matchCatalog(ctx, providerID, mergeModels(lists, serving))
		case providerID == s.defaultProvider():
			// No account to ask: the public catalogue, so the aliases, the
			// configs and the docs page have models to name before one is
			// connected. Nothing can be served until then either way. Only
			// for the default provider: Anthropic's /v1/models refuses a
			// caller with no account token, and models.dev's other vendors
			// are their metered APIs, not what a subscription serves.
			if models := s.catalogModels(ctx, providerID); models != nil {
				part, fromCatalog = catalogList(models), true
			}
		}
		if part == nil {
			continue
		}
		seen.rememberProvider(providerID, part)
		parts = append(parts, part)
	}
	s.remember(seen, len(parts) > 0)
	if len(parts) == 0 {
		if lastErr == nil {
			lastErr = pool.ErrNoAccounts
		}
		return nil, false, lastErr
	}
	return joinLists(parts), fromCatalog, nil
}

// hasAccounts reports whether any account of a provider is switched on.
func hasAccounts(accounts []store.Account, providerID string) bool {
	for _, a := range accounts {
		if a.Provider == providerID && !a.Disabled() {
			return true
		}
	}
	return false
}

// joinLists is several providers' lists as one, in the providers' order. One
// list is returned as it stands — the same bytes — which is the gateway with
// one provider connected, and costs nothing to keep exact.
func joinLists(parts [][]byte) []byte {
	if len(parts) == 1 {
		return parts[0]
	}
	var data []json.RawMessage
	for _, p := range parts {
		var l struct {
			Data []json.RawMessage `json:"data"`
		}
		if json.Unmarshal(p, &l) == nil {
			data = append(data, l.Data...)
		}
	}
	list := map[string]any{"data": data, "has_more": false}
	ids := make([]string, 0, 2)
	for _, i := range []int{0, len(data) - 1} {
		if i < 0 || i >= len(data) {
			continue
		}
		var m struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(data[i], &m) == nil {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 2 {
		list["first_id"], list["last_id"] = ids[0], ids[1]
	}
	out, err := json.Marshal(list)
	if err != nil {
		return parts[0]
	}
	return out
}

// ListFor is the model list as one account sees it, unfiltered: the
// models its own subscription serves, which is what that account's switches
// are switches over. Subscriptions differ, so this is asked per account.
func (s *Catalog) ListFor(ctx context.Context, accountID, rawQuery string) ([]byte, error) {
	a, err := s.store.Account(ctx, accountID)
	if err != nil {
		return nil, err
	}
	r := s.relayFor(a.Provider)
	if r == nil {
		return nil, fmt.Errorf("this gateway does not serve the %s provider", a.Provider)
	}
	token, err := s.pool.AccessToken(ctx, accountID)
	if err != nil {
		return nil, err
	}
	body, err := s.listModels(ctx, r, a, token, rawQuery)
	if err != nil {
		return nil, err
	}
	// Which models is the account's to say; what they are, the catalogue's,
	// under the provider's own name there.
	return s.matchCatalog(ctx, a.Provider, body), nil
}

// maxModelList bounds an account's list. Anthropic's is a few kilobytes;
// ChatGPT's carries each model's whole instructions and was 573 KB on
// 2026-10-19.
const maxModelList = 8 << 20

// listModels asks the upstream for its models with one account's credential.
//
// Addressed, authorised and read by the provider's wire, as a relayed request
// is, so the provider's address, headers and list shape stay in its package.
func (s *Catalog) listModels(ctx context.Context, r *relay.Relay, a store.Account, token, rawQuery string) ([]byte, error) {
	base := r.BaseURL
	if base == "" {
		base = r.Wire.BaseURL()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+r.Wire.ModelsPath(rawQuery), nil)
	if err != nil {
		return nil, err
	}
	r.Wire.Authorize(req, provider.Credential{Token: token, AccountID: a.AccountUUID})

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream models returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelList))
	if err != nil {
		return nil, err
	}
	return r.Wire.ModelList(body)
}

// mergeModels is the union of several accounts' model lists, each less the
// models switched off on its own account, in the order the lists came: the
// first account's models as it lists them, then any the others add.
//
// The first list is returned as it stands — the same bytes — when it is the
// only one and its account has nothing off, which is the usual case and costs
// nothing to keep exact. Otherwise the result is the first list's envelope
// with its data replaced, and the paging fields made true of what is in it.
func mergeModels(lists [][]byte, accounts []store.Account) []byte {
	if len(lists) == 1 && len(accounts[0].ModelsOff) == 0 {
		return lists[0]
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(lists[0], &envelope); err != nil {
		return lists[0]
	}
	seen := map[string]bool{}
	var (
		kept          []json.RawMessage
		first, latest string
	)
	for i, body := range lists {
		var list struct {
			Data []json.RawMessage `json:"data"`
		}
		if json.Unmarshal(body, &list) != nil {
			continue
		}
		for _, e := range list.Data {
			var m struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(e, &m) != nil || m.ID == "" {
				continue
			}
			key := store.ModelKey(m.ID)
			if seen[key] || !accounts[i].Serves(m.ID) {
				continue
			}
			seen[key] = true
			kept = append(kept, e)
			if first == "" {
				first = m.ID
			}
			latest = m.ID
		}
	}
	if kept == nil {
		kept = []json.RawMessage{}
	}
	set := func(field string, v any) {
		if raw, err := json.Marshal(v); err == nil {
			envelope[field] = raw
		}
	}
	set("data", kept)
	// Every account's list was asked for in full, so this is all of it.
	set("has_more", false)
	if first != "" {
		set("first_id", first)
		set("last_id", latest)
	}
	out, err := json.Marshal(envelope)
	if err != nil {
		return lists[0]
	}
	return out
}
