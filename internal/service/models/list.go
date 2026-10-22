// Package models is the gateway's model list: what it serves, under which
// names, with which facts, and which upstream serves each — one answer for
// /v1/models, the admin screens, the configs the admin UI writes and the
// routing of every request.
//
// It owns the four things that answer is made of: the upstream lists, asked
// of each account of each provider (list.go); the public catalogue those
// lists are matched against (catalog.go); the moving targets a client's
// slots can follow — the newest model of each Claude family, the ChatGPT
// backend's ranking; and the route a requested name takes (route.go). From
// them it says what each client may be offered (available.go), the one rule
// for every surface that offers a model.
//
// A service of its own because two surfaces read it and neither owns it. It
// lived in the gateway, which handed it to the admin API as five separate
// functions; each surface now takes the narrow part of it that it reads.
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
	"sync"
	"time"

	"claudication/internal/config"
	"claudication/internal/pool"
	"claudication/internal/provider"
	"claudication/internal/relay"
	"claudication/internal/service/clients"
	"claudication/internal/store"
)

// Store is what the catalogue reads: the accounts it asks, and the clients'
// model names.
type Store interface {
	ListAccounts(ctx context.Context) ([]store.Account, error)
	Account(ctx context.Context, id string) (store.Account, error)
	ClientSlotNamed(ctx context.Context, keyID, name string) (store.ClientSlot, bool, error)
	ClientSlots(ctx context.Context, keyID string) ([]store.ClientSlot, error)
}

// Pool is what the catalogue asks of the account pool: a token to read an
// account's list with, and whether any account serves a model now.
type Pool interface {
	AccessToken(ctx context.Context, id string) (string, error)
	Offers(ctx context.Context, provider, model string) bool
}

// Catalog is the model list and the moving targets over it.
type Catalog struct {
	cfg        *config.Config
	log        *slog.Logger
	store      Store
	pool       Pool
	relays     []*relay.Relay
	httpClient *http.Client

	// What the accounts' lists last said, for the follow targets and the routes;
	// see remember.
	rolesMu sync.Mutex
	// newest is the newest model of each Claude family, and families the
	// families newest release first, for the claude:<family> targets.
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
	// caps is each provider's models' output caps, by store.ModelKey, as its
	// list says them (the catalogue's limit.output over the account's own):
	// what a slot's request has its budget fitted to (OutputCap).
	caps map[string]map[string]int
	// listed is what the accounts themselves said, the catalogue never:
	// every model a switched-on account of each provider lists, switches or
	// not, in the lists' order and by store.ModelKey. Which of them a client
	// can be offered is read against the accounts as they are now
	// (available.go), since a switch takes effect on the next request and
	// these lists are kept for newestTTL.
	listed   map[string][]listedModel
	newestAt time.Time

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

// For is the model list one key is offered — /v1/models as that key sees it,
// and what the admin UI writes its client's configs from, so the two cannot
// disagree.
//
// A client that names its own slots is offered those and nothing else, each
// carrying the facts of the model it serves now: a model on its menu that is
// refused when picked is worse than one not offered. A custom client in
// direct mode is offered the real models, less what it may not use. Either
// way only what is available to the client now (available.go): a slot whose
// binding has become unavailable is left out until it is rebound, and before
// the first sign-in a client is offered nothing.
//
// Every provider's models are in it, and every client-facing API reaches
// every provider; a ChatGPT one carries "provider": "openai", and so does a
// slot serving one, which is how a reader tells them apart.
func (s *Catalog) For(ctx context.Context, key store.APIKey) ([]byte, error) {
	body, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	if !key.NamesSlots() {
		// The gateway's own key is no client: it is offered the list as it
		// stands, and the request says what it gets.
		if key.ClientType != "" {
			body = s.onlyAvailable(ctx, body)
		}
		// Less what the key may not use, for the reason the switched-off
		// models are left out.
		if len(key.ModelsOff) > 0 {
			body = modelsAllowedFor(body, key)
		}
		return body, nil
	}
	slots, err := s.store.ClientSlots(ctx, key.ID)
	if err != nil {
		return nil, err
	}
	var entries []virtualEntry
	own := map[string]bool{}
	for _, sl := range slots {
		c, why := s.Resolve(ctx, store.Binding{Model: sl.Model, Follows: sl.Follows})

		if why != "" {
			continue
		}
		entries = append(entries, virtualEntry{
			ID: sl.Name, DisplayName: sl.Name,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
			Serving:   c.Serving, Slot: sl.Slot,
		})
		own[sl.Name] = true
	}
	if len(entries) > 0 {
		body = withVirtual(body, entries)
	}
	return onlyModels(body, own), nil
}

// virtualEntry is a client's model name as a model list shows it. The entry
// is the serving model's own, with the id and name changed — so its context
// window, output limit and capabilities are the real ones, and a client
// sizing its requests from the list sizes them right — plus fields of the
// gateway's, which clients ignore and the admin UI and config writer read.
type virtualEntry struct {
	ID          string
	DisplayName string
	CreatedAt   string
	Serving     string
	// Slot is the slot it fills, for the config writer.
	Slot string
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
	caps      map[string]map[string]int
	connected map[string]bool
	unread    map[string]bool
	listed    map[string][]listedModel
}

// listedModel is one model an account offers, as a binding menu names it.
type listedModel struct {
	ID, Name string
}

// rememberAccounts notes what one provider's accounts said themselves: every
// model any of them lists, switches or not, first account first.
func (m *remembered) rememberAccounts(providerID string, lists [][]byte) {
	if m.listed == nil {
		m.listed = map[string][]listedModel{}
	}
	seen := map[string]bool{}
	var out []listedModel
	for _, l := range lists {
		for _, e := range listEntries(l) {
			if k := store.ModelKey(e.ID); !seen[k] {
				seen[k] = true
				out = append(out, e)
			}
		}
	}
	m.listed[providerID] = out
}

// listEntries are a model list's real models, as listed.
func listEntries(list []byte) []listedModel {
	var l struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			Virtual     bool   `json:"virtual"`
		} `json:"data"`
	}
	if json.Unmarshal(list, &l) != nil {
		return nil
	}
	out := make([]listedModel, 0, len(l.Data))
	for _, e := range l.Data {
		if e.ID != "" && !e.Virtual {
			out = append(out, listedModel{ID: e.ID, Name: e.DisplayName})
		}
	}
	return out
}

// rememberProvider notes what one provider's list says: which models it
// serves, and, for the providers whose targets follow it, the newest of each
// Claude family or the ChatGPT ranking.
func (m *remembered) rememberProvider(providerID string, list []byte) {
	var l struct {
		Data []struct {
			ID        string `json:"id"`
			CreatedAt string `json:"created_at"`
			Virtual   bool   `json:"virtual"`
			Priority  *int   `json:"priority"`
			MaxTokens int    `json:"max_tokens"`
		} `json:"data"`
	}
	if json.Unmarshal(list, &l) != nil {
		return
	}
	if m.served == nil {
		m.served = map[string]map[string]bool{}
	}
	if m.caps == nil {
		m.caps = map[string]map[string]int{}
	}
	ids := map[string]bool{}
	caps := map[string]int{}
	for _, e := range l.Data {
		if !e.Virtual && e.ID != "" {
			ids[store.ModelKey(e.ID)] = true
			if e.MaxTokens > 0 {
				caps[store.ModelKey(e.ID)] = e.MaxTokens
			}
		}
	}
	m.served[providerID] = ids
	m.caps[providerID] = caps

	if providerID == clients.FollowFirst.Provider() {
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
	if providerID != clients.FollowOpus.Provider() {
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
	// so one upstream's bad minute does not empty its targets.
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
		if old, ok := s.caps[p]; ok {
			if m.caps == nil {
				m.caps = map[string]map[string]int{}
			}
			m.caps[p] = old
		}
		if old, ok := s.listed[p]; ok {
			if m.listed == nil {
				m.listed = map[string][]listedModel{}
			}
			m.listed[p] = old
		}
		if p == clients.FollowFirst.Provider() && len(m.ranked) == 0 {
			m.ranked = s.ranked
		}
		if p == clients.FollowOpus.Provider() && m.newest == nil {
			m.newest, m.families = s.newest, s.families
		}
	}
	s.newest, s.families, s.ranked, s.served = m.newest, m.families, m.ranked, m.served
	s.caps = m.caps
	s.listed = m.listed
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

// OutputCap is the most a model of a provider can write in one answer, as
// the model list last said it, and 0 when it did not say: what a request
// naming a slot has its output budget fitted to (internal/relay/passes,
// FitMaxTokens).
//
// Read from what the lists last said, stale or not, and not refreshed for
// it: a model's cap changes with a release, every few months, and a request
// for a Claude model must not wait on a round of model lists to learn it
// (ProviderOf says the same). Only a gateway that has read no list since it
// started reads them now, once, so its first request after a restart is
// fitted like the rest.
func (s *Catalog) OutputCap(ctx context.Context, provider, model string) int {
	s.rolesMu.Lock()
	never := s.newestAt.IsZero()
	s.rolesMu.Unlock()
	if never {
		s.refreshFamilies(ctx)
	}
	s.rolesMu.Lock()
	defer s.rolesMu.Unlock()
	return s.caps[provider][store.ModelKey(model)]
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

// Families are the Claude families the accounts list, newest release first:
// the claude:<family> targets a slot can follow.
func (s *Catalog) Families(ctx context.Context) []string {
	s.refreshFamilies(ctx)
	s.rolesMu.Lock()
	defer s.rolesMu.Unlock()
	return append([]string(nil), s.families...)
}

// gptRanked is what the openai: targets follow, from the backend's own
// ranking of the models a ChatGPT account lists (the priority each entry
// carries, which is the order Codex's model picker shows them in):
//
//   - openai:first is the first-ranked: the backend's own first choice.
//     2026-10-19: gpt-6.1-sol, "Latest workhorse model for coding and
//     everyday work", priority 1.
//   - openai:light is the last-ranked model of the first-ranked one's
//     generation — the run of models at the top of the ranking sharing its
//     major version, and the last of them. The backend ranks its smaller
//     models of a generation below its larger ones and the previous
//     generation below both, so this is the current generation's light
//     model rather than an old one. 2026-10-19: gpt-6-luna, "Fast and
//     affordable model for easier tasks", priority 5 — the same model the
//     backend itself named, in x-codex-safety-buffering-faster-model, as its
//     faster one.
//
// When the first-ranked id carries no version to compare, light is the
// last-ranked model of all.
func (s *Catalog) gptRanked(ctx context.Context, first bool) string {
	s.refreshFamilies(ctx)
	s.rolesMu.Lock()
	ranked := append([]string(nil), s.ranked...)
	s.rolesMu.Unlock()
	if len(ranked) == 0 {
		return ""
	}
	if first {
		return ranked[0]
	}
	top := generation.FindStringSubmatch(ranked[0])
	if top == nil {
		return ranked[len(ranked)-1]
	}
	light := ranked[0]
	for _, id := range ranked[1:] {
		g := generation.FindStringSubmatch(id)
		if g == nil || g[1] != top[1] {
			break
		}
		light = id
	}
	return light
}

// FollowTarget is the real model a moving target points at now, from its
// provider's list; why says so when the list names none.
func (s *Catalog) FollowTarget(ctx context.Context, f clients.Follow) (target, why string) {
	switch {
	case f.Family() != "":
		if target = s.newestOf(ctx, f.Family()); target == "" {
			return "", "no account lists a Claude " + f.Family() + " model"
		}
	case f == clients.FollowFirst || f == clients.FollowLight:
		if target = s.gptRanked(ctx, f == clients.FollowFirst); target == "" {
			return "", "no ChatGPT account lists a model"
		}
	default:
		return "", "there is no target " + string(f) + " to follow"
	}
	return target, ""
}

// SlotTarget is the real model one of a client's slots serves now: the model
// it is bound to, or what the target it follows points at. why is non-empty
// when it cannot serve, naming the slot, the model and what chose it.
func (s *Catalog) SlotTarget(ctx context.Context, sl store.ClientSlot) (target, why string) {
	target, chose := sl.Model, ""
	if target == "" {
		if target, why = s.FollowTarget(ctx, sl.Follows); why != "" {
			return "", sl.Name + " follows " + sl.Follows.Title() + ", and " + why
		}
		chose = " (" + sl.Follows.Title() + ")"
	}
	if !s.pool.Offers(ctx, s.ServingProvider(ctx, target), target) {
		return target, sl.Name + " is " + target + chose + ", which no connected account has on"
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
		fields["slot"] = e.Slot
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

// onlyModels is a model list with only the entries whose ids are in keep. A
// list it cannot read is returned with nothing in it: what it would have
// offered is refused anyway.
func onlyModels(body []byte, keep map[string]bool) []byte {
	var list map[string]json.RawMessage
	if err := json.Unmarshal(body, &list); err != nil {
		return []byte(`{"data":[],"has_more":false}`)
	}
	var data []json.RawMessage
	_ = json.Unmarshal(list["data"], &data)
	kept := make([]json.RawMessage, 0, len(keep))
	var ids []string
	for _, m := range data {
		var model struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(m, &model) == nil && keep[model.ID] {
			kept = append(kept, m)
			ids = append(ids, model.ID)
		}
	}
	set := func(field string, v any) {
		if raw, err := json.Marshal(v); err == nil {
			list[field] = raw
		}
	}
	set("data", kept)
	set("has_more", false)
	if len(ids) > 0 {
		set("first_id", ids[0])
		set("last_id", ids[len(ids)-1])
	} else {
		delete(list, "first_id")
		delete(list, "last_id")
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
// the admin UI and the configs it writes alike.
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
	accounts, err := s.store.ListAccounts(ctx)
	if err != nil {
		return nil, err
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
			seen.rememberAccounts(providerID, lists)
		case providerID == s.defaultProvider():
			// No account to ask: the public catalogue, so the admin screens
			// have models to show before one is connected. Nothing can be
			// served until then, and no client is offered any of it
			// (available.go reads the accounts' own lists). Only
			// for the default provider: Anthropic's /v1/models refuses a
			// caller with no account token, and models.dev's other vendors
			// are their metered APIs, not what a subscription serves.
			if models := s.catalogModels(ctx, providerID); models != nil {
				part = catalogList(models)
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
		return nil, lastErr
	}
	return joinLists(parts), nil
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
