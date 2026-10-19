// Package gateway is the client-facing half of the HTTP surface: the APIs a
// client points at, with the authentication, limits and accounting around
// them. What a request then becomes upstream is the relay's business.
package gateway

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

	"claudication/internal/api"
	anthropicapi "claudication/internal/api/anthropic"
	"claudication/internal/api/openai"
	"claudication/internal/config"
	"claudication/internal/httpapi/httpx"
	"claudication/internal/memlimit"
	"claudication/internal/pool"
	"claudication/internal/relay"
	"claudication/internal/service/install"
	"claudication/internal/service/limits"
	"claudication/internal/service/surfaces"
	"claudication/internal/service/titles"
	usagesvc "claudication/internal/service/usage"
	"claudication/internal/store"
)

// Gateway serves the client-facing APIs: it authenticates a key, holds it to
// its limits and budget, and hands the request to the relay in whichever
// dialect it arrived.
type Gateway struct {
	cfg         *config.Config
	log         *slog.Logger
	store       *store.Store
	keyLimiter  *limits.Limiter
	anonLimiter *limits.Limiter
	budgets     *limits.Budgets
	recorder    *usagesvc.Recorder
	pool        *pool.Pool
	relay       *relay.Relay
	titles      *titles.Titler
	trimmer     *memlimit.Trimmer
	surfaces    *surfaces.Surfaces
	protocols   api.Registry
	httpClient  *http.Client
	install     *install.Links

	// The newest model of each family, for automatic model roles; see
	// newestOf.
	rolesMu  sync.Mutex
	newest   map[string]string
	families []string
	newestAt time.Time

	// The public model catalogue; see catalog.go.
	catalog catalog
}

// Deps is what a Gateway is built from. Every field is required.
type Deps struct {
	Config      *config.Config
	Log         *slog.Logger
	Store       *store.Store
	KeyLimiter  *limits.Limiter
	AnonLimiter *limits.Limiter
	Budgets     *limits.Budgets
	Recorder    *usagesvc.Recorder
	Pool        *pool.Pool
	Relay       *relay.Relay
	Titles      *titles.Titler
	Trimmer     *memlimit.Trimmer
	Surfaces    *surfaces.Surfaces
	Protocols   api.Registry
	// HTTPClient makes the gateway's own small upstream calls: the model list.
	HTTPClient *http.Client
	// Install keeps the one-time install bundles /install serves.
	Install *install.Links
}

// New builds a Gateway.
func New(d Deps) *Gateway {
	return &Gateway{
		cfg: d.Config, log: d.Log, store: d.Store,
		keyLimiter: d.KeyLimiter, anonLimiter: d.AnonLimiter, budgets: d.Budgets,
		recorder: d.Recorder, pool: d.Pool, relay: d.Relay, titles: d.Titles,
		trimmer: d.Trimmer, surfaces: d.Surfaces, protocols: d.Protocols,
		httpClient: d.HTTPClient, install: d.Install,
	}
}

// Routes registers the client-facing APIs on mux.
//
// Each is an api.Protocol and each is gated on its own switch, so one can be
// turned off without touching the other and both can be turned off at once.
//
// The upstream path is the same for both, because there is only one thing on
// the other end: /v1/messages is what a subscription account answers,
// whatever shape the caller asked in.
func (s *Gateway) Routes(mux *http.ServeMux) {
	messages, _ := s.protocols.Find(anthropicapi.ID)
	responses, _ := s.protocols.Find(openai.ID)

	// Model discovery belongs to the Anthropic surface alone. /v1/models
	// is a path both APIs define with different answers, and Codex never
	// asks — it is configured with a model name — so there is nothing to
	// gain by guessing which dialect a caller meant.
	// The one-line installer's script. On the relay, because the address a
	// client is configured with is the one its machine can already reach, and
	// outside any API's switch: it is not one. See install.go.
	mux.HandleFunc("GET /install", s.handleInstall)

	mux.Handle("GET /v1/models",
		s.surface(messages, s.requireAPIKey(messages, http.HandlerFunc(s.handleModels))))
	mux.Handle("POST /v1/messages",
		s.surface(messages, s.requireAPIKey(messages,
			s.inference(messages, "/v1/messages", "/v1/messages?beta=true"))))

	// Serving count_tokens matters, and the client says so in its own
	// code: when a gateway answers 501 here, it falls back to measuring
	// the context by issuing a real max_tokens:1 inference request and
	// reading the usage off it. That is a billed request spent on
	// arithmetic, once per measurement. Removing this route because
	// "nothing seems to call it" would turn that on silently.
	mux.Handle("POST /v1/messages/count_tokens",
		s.surface(messages, s.requireAPIKey(messages,
			s.inference(messages, "/v1/messages/count_tokens",
				"/v1/messages/count_tokens?beta=true"))))

	mux.Handle("POST /v1/responses",
		s.surface(responses, s.requireAPIKey(responses,
			s.inference(responses, "/v1/responses", "/v1/messages?beta=true"))))
}

// handleModels answers the discovery request by relaying Anthropic's own model
// list, so it stays correct as models come and go.
//
// The contract pins the shape hard: Claude Code sends GET /v1/models?limit=1000
// with a 3-second timeout, treats any redirect as failure, and keeps only ids
// containing "claude" or "anthropic".
//
// A failure must be answered as a failure. The client falls back to its cached
// list only when the request fails; a 200 carrying an empty data array is a
// successful answer that says "this gateway serves no models", so it overwrites
// the good cache with nothing and the model picker goes empty. Being unable to
// reach the upstream is a 502, and the client recovers on its own.
func (s *Gateway) handleModels(w http.ResponseWriter, r *http.Request) {
	// Comfortably inside the client's 3-second budget.
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	key, _ := httpx.APIKey(r.Context())
	body, err := s.ModelsFor(ctx, key)
	if err != nil {
		s.log.Warn("model discovery failed; answering 502 so the client keeps its cached list",
			"err", err, "request_id", httpx.RequestID(r.Context()))
		httpx.WriteError(w, http.StatusBadGateway, "api_error", "could not reach the upstream model list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// ModelsFor is the model list one key is offered: the two model roles first,
// each carrying the facts of the model it serves for that key, then the real
// models, less what the key may not use. The /v1/models answer, and what the
// admin UI writes that key's configs from, so the two cannot disagree.
//
// A role is listed only while it can serve.
func (s *Gateway) ModelsFor(ctx context.Context, key store.APIKey) ([]byte, error) {
	body, fromCatalog, err := s.fetchModels(ctx)
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
func (s *Gateway) rememberNewest(list []byte) {
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
func (s *Gateway) refreshFamilies(ctx context.Context) {
	s.rolesMu.Lock()
	stale := time.Since(s.newestAt) >= newestTTL
	s.rolesMu.Unlock()
	if !stale {
		return
	}
	if body, err := s.FetchModels(ctx); err == nil {
		s.rememberNewest(body)
	}
}

// newestOf is the newest model of a family.
func (s *Gateway) newestOf(ctx context.Context, family string) string {
	s.refreshFamilies(ctx)
	s.rolesMu.Lock()
	defer s.rolesMu.Unlock()
	return s.newest[family]
}

// Aliases is every alias the gateway answers to now: the two roles, one per
// family the accounts list (newest release first), and the operator's own.
func (s *Gateway) Aliases(ctx context.Context) ([]store.Alias, error) {
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

// isAlias reports whether a request's model is one of the aliases. A real id
// always carries a version, so only a version-less claude-<family> has to be
// checked against the families, and the model list is not read for a
// request naming a real model.
func (s *Gateway) isAlias(ctx context.Context, model string) bool {
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
func (s *Gateway) RoleTarget(ctx context.Context, key store.APIKey, role string) (target, why string) {
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

// FetchModels is the models this gateway serves: one answer for /v1/models,
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
func (s *Gateway) FetchModels(ctx context.Context) ([]byte, error) {
	body, _, err := s.fetchModels(ctx)
	return body, err
}

// fetchModels is FetchModels, saying whether the list is the catalogue's
// because no account's could be read.
func (s *Gateway) fetchModels(ctx context.Context) (body []byte, fromCatalog bool, err error) {
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
		body, err := s.FetchModelsFor(ctx, a.ID, "limit=1000")
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

// FetchModelsFor is the model list as one account sees it, unfiltered: the
// models its own subscription serves, which is what that account's switches
// are switches over. Subscriptions differ, so this is asked per account.
func (s *Gateway) FetchModelsFor(ctx context.Context, accountID, rawQuery string) ([]byte, error) {
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
func (s *Gateway) listModels(ctx context.Context, token, rawQuery string) ([]byte, error) {
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

// surface gates a route on its API being switched on.
//
// It answers 404 rather than 503 on purpose. 503 says "try again shortly" and
// a client obediently will, for as long as the surface stays off; 404 says the
// gateway does not serve this, which is the truth and which stops the client
// rather than making it spin. The message names the surface and where to turn
// it back on, because the operator reading it in a client's log is usually not
// the one who flipped the switch.
//
// The check is per request and reads a cached value, so a switch takes effect
// on the next request with no restart and no lock on the hot path: each
// switch is an atomic.
func (s *Gateway) surface(p api.Protocol, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.surfaces.Enabled(p.ID()) {
			p.WriteError(w, http.StatusNotFound, "not_found",
				"the "+p.Title()+" is turned off on this gateway; "+
					"an administrator can switch it back on under Settings")
			return
		}
		next.ServeHTTP(w, r)
	})
}
