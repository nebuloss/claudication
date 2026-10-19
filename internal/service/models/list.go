// Package models is the gateway's model list: what it serves, under which
// names, with which facts — one answer for /v1/models, the admin screens, the
// configs the admin UI writes and the public docs page.
//
// It owns the three things that answer is made of: the upstream lists, asked
// of each account (list.go); the public catalogue those lists are matched
// against (catalog.go); and the aliases, which are the newest model of each
// family the accounts list, the two roles, and the operator's own.
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
	"claudication/internal/relay"
	"claudication/internal/store"
)

// Store is what the catalogue reads: the accounts it asks, and the aliases
// and bindings the operator stored.
type Store interface {
	ListAccounts(ctx context.Context) ([]store.Account, error)
	CustomAliases(ctx context.Context) ([]string, error)
	RoleBinding(ctx context.Context, keyID, role string) (string, error)
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
	relay      *relay.Relay
	httpClient *http.Client

	// The newest model of each family, for automatic model roles; see
	// newestOf.
	rolesMu  sync.Mutex
	newest   map[string]string
	families []string
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
	// Relay is whose wire an account's list is read over, so the provider's
	// address and headers stay in the provider's package.
	Relay *relay.Relay
	// HTTPClient makes the small upstream calls: the lists and the catalogue.
	HTTPClient *http.Client
}

// New builds a Catalog. Nothing is read until it is asked.
func New(d Deps) *Catalog {
	return &Catalog{
		cfg: d.Config, log: d.Log, store: d.Store, pool: d.Pool,
		relay: d.Relay, httpClient: d.HTTPClient,
	}
}

// Public is the list as the public docs page shows it: as a key that binds no
// alias and is kept off nothing sees it, so the configs written from it ask
// for the roles as every other config does.
func (s *Catalog) Public(ctx context.Context) ([]byte, error) {
	return s.For(ctx, store.APIKey{})
}

// For is the model list one key is offered: the two model roles first,
// each carrying the facts of the model it serves for that key, then the real
// models, less what the key may not use. The /v1/models answer, and what the
// admin UI writes that key's configs from, so the two cannot disagree.
//
// A role is listed only while it can serve.
func (s *Catalog) For(ctx context.Context, key store.APIKey) ([]byte, error) {
	body, fromCatalog, err := s.list(ctx)
	if err != nil {
		return nil, err
	}
	s.rememberNewest(body)
	aliases, err := s.Aliases(ctx)
	if err != nil {
		return nil, err
	}
	var entries []virtualEntry
	for _, a := range aliases {
		// Listed from the catalogue, no account can serve anything yet, and
		// an alias is shown as it will serve once one is connected — the
		// configs written now are the ones that will be used then.
		target, why := s.RoleTarget(ctx, key, a.Name)
		if fromCatalog && target != "" && key.Allows(target) {
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
	// sonnet, haiku, fable — for the config writer.
	Role string
}

// roleLabel is how an alias reads in a client's model picker.
func roleLabel(a store.Alias) string {
	name := strings.TrimPrefix(a.Name, "claude-")
	switch {
	case store.IsRoleName(a.Name):
		return "Claude (" + name + ")"
	case a.Builtin:
		return "Claude " + strings.ToUpper(name[:1]) + name[1:]
	}
	return a.Name
}

// newestTTL is how long the families and their newest models are remembered.
// The list comes from the upstream, once per account, and a model is released
// every few months; a request should not wait on it.
const newestTTL = 10 * time.Minute

// familyOf is a real model's family, from its id: claude-opus-5-5 is opus.
var familyOf = regexp.MustCompile(`^claude-([a-z]+)-`)

// rememberNewest notes every family in a model list and the newest model of
// each, by release date — not by the list's order, and not from a list of
// families written down here: a family Anthropic adds is found the day an
// account lists it. Families are kept newest release first.
func (s *Catalog) rememberNewest(list []byte) {
	var l struct {
		Data []struct {
			ID        string `json:"id"`
			CreatedAt string `json:"created_at"`
			Virtual   bool   `json:"virtual"`
		} `json:"data"`
	}
	if json.Unmarshal(list, &l) != nil {
		return
	}
	type pick struct {
		id, at string
		seen   int // where the family first appears, for ties
	}
	newest := map[string]pick{}
	for i, m := range l.Data {
		match := familyOf.FindStringSubmatch(m.ID)
		if m.Virtual || match == nil {
			continue
		}
		// RFC 3339 times in one zone order as strings do. A later entry
		// wins only when strictly newer, so a list that already runs newest
		// first keeps its first.
		have, ok := newest[match[1]]
		if !ok {
			newest[match[1]] = pick{m.ID, m.CreatedAt, i}
		} else if m.CreatedAt > have.at {
			newest[match[1]] = pick{m.ID, m.CreatedAt, have.seen}
		}
	}
	families := make([]string, 0, len(newest))
	ids := make(map[string]string, len(newest))
	for f, p := range newest {
		families = append(families, f)
		ids[f] = p.id
	}
	sort.Slice(families, func(i, j int) bool {
		a, b := newest[families[i]].at, newest[families[j]].at
		if a != b {
			return a > b
		}
		// Undated, or released together: the accounts' own order, newest first.
		return newest[families[i]].seen < newest[families[j]].seen
	})
	s.rolesMu.Lock()
	s.newest, s.families, s.newestAt = ids, families, time.Now()
	s.rolesMu.Unlock()
}

// refreshFamilies re-reads the model list when what was remembered is stale.
func (s *Catalog) refreshFamilies(ctx context.Context) {
	s.rolesMu.Lock()
	stale := time.Since(s.newestAt) >= newestTTL
	s.rolesMu.Unlock()
	if !stale {
		return
	}
	if body, err := s.List(ctx); err == nil {
		s.rememberNewest(body)
	}
}

// newestOf is the newest model of a family.
func (s *Catalog) newestOf(ctx context.Context, family string) string {
	s.refreshFamilies(ctx)
	s.rolesMu.Lock()
	defer s.rolesMu.Unlock()
	return s.newest[family]
}

// Aliases is every alias the gateway answers to now: the two roles, one per
// family the accounts list (newest release first), and the operator's own.
func (s *Catalog) Aliases(ctx context.Context) ([]store.Alias, error) {
	s.refreshFamilies(ctx)
	out := []store.Alias{
		{Name: store.RoleLarge, Family: store.AliasFamily(store.RoleLarge), Builtin: true},
		{Name: store.RoleSmall, Family: store.AliasFamily(store.RoleSmall), Builtin: true},
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

// RoleTarget is the real model an alias serves for a key: the key's binding,
// else the gateway's, else — for a built-in — the newest of its family. why
// is non-empty when it cannot serve — the key may not use it, or no account
// has it on — and says so, naming the model.
func (s *Catalog) RoleTarget(ctx context.Context, key store.APIKey, role string) (target, why string) {
	role = store.ModelKey(role)
	bound, err := s.store.RoleBinding(ctx, key.ID, role)
	if err != nil {
		return "", "could not read the model aliases"
	}
	target = bound
	if target == "" {
		family := store.AliasFamily(role)
		if family == "" {
			return "", role + " points to no model"
		}
		target = s.newestOf(ctx, family)
		if target == "" {
			return "", role + " follows the newest " + family + " model, and no account lists one"
		}
	}
	if !key.Allows(target) {
		return target, role + " is " + target + ", which this API key may not use"
	}
	if !s.pool.Offers(ctx, "anthropic", target) {
		return target, role + " is " + target + ", which no connected account has on"
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
// off on that account. Per account, because subscriptions differ: a model
// only the second account's plan has is still a model the gateway serves, and
// asking only the first would leave it out. And less the switches, because a
// model off everywhere is one a client would only meet as a refusal; clients
// that build their menus from this list — Claude Code, and the configs the
// admin UI writes from it — leave it out instead.
//
// One upstream call per account, in priority order, each for its whole list:
// a client's paging asked of every account separately would page nothing it
// could follow, so the answer is always all of it. An account whose list cannot
// be read is skipped; only when none can is it an error.
func (s *Catalog) List(ctx context.Context) ([]byte, error) {
	body, _, err := s.list(ctx)
	return body, err
}

// list is List, saying whether the list is the catalogue's
// because no account's could be read.
func (s *Catalog) list(ctx context.Context) (body []byte, fromCatalog bool, err error) {
	accounts, err := s.store.ListAccounts(ctx)
	if err != nil {
		return nil, false, err
	}
	var (
		lists   [][]byte
		serving []store.Account
		lastErr error
	)
	for _, a := range accounts {
		if a.Provider != "anthropic" || a.Disabled() {
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
	if len(lists) == 0 {
		// No account to ask: the public catalogue, so the aliases, the
		// configs and the docs page have models to name before one is
		// connected. Nothing can be served until then either way.
		if models := s.catalogModels(ctx, "anthropic"); models != nil {
			return catalogList(models), true, nil
		}
		if lastErr == nil {
			lastErr = pool.ErrNoAccounts
		}
		return nil, false, lastErr
	}
	// Matched again over the union: each account's list was matched on its
	// own, and the union runs first account first.
	return s.matchCatalog(ctx, "anthropic", mergeModels(lists, serving)), false, nil
}

// ListFor is the model list as one account sees it, unfiltered: the
// models its own subscription serves, which is what that account's switches
// are switches over. Subscriptions differ, so this is asked per account.
func (s *Catalog) ListFor(ctx context.Context, accountID, rawQuery string) ([]byte, error) {
	token, err := s.pool.AccessToken(ctx, accountID)
	if err != nil {
		return nil, err
	}
	body, err := s.listModels(ctx, token, rawQuery)
	if err != nil {
		return nil, err
	}
	// Which models is the account's to say; what they are, the catalogue's.
	// The relay's wire is Anthropic's, and so is every account it reads.
	return s.matchCatalog(ctx, "anthropic", body), nil
}

// listModels asks the upstream for its models with one account's token.
//
// Addressed and authorised by the relay's wire, as a relayed request is, so
// the provider's address and headers stay in the provider's package.
func (s *Catalog) listModels(ctx context.Context, token, rawQuery string) ([]byte, error) {
	base := s.relay.BaseURL
	if base == "" {
		base = s.relay.Wire.BaseURL()
	}
	url := base + "/v1/models"
	if rawQuery != "" {
		url += "?" + rawQuery
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	s.relay.Wire.Authorize(req, token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream models returned %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
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
