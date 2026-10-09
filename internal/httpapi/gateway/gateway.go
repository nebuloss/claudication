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
	"time"

	"claudication/internal/api"
	anthropicapi "claudication/internal/api/anthropic"
	"claudication/internal/api/openai"
	"claudication/internal/config"
	"claudication/internal/httpapi/httpx"
	"claudication/internal/memlimit"
	"claudication/internal/pool"
	"claudication/internal/relay"
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
}

// New builds a Gateway.
func New(d Deps) *Gateway {
	return &Gateway{
		cfg: d.Config, log: d.Log, store: d.Store,
		keyLimiter: d.KeyLimiter, anonLimiter: d.AnonLimiter, budgets: d.Budgets,
		recorder: d.Recorder, pool: d.Pool, relay: d.Relay, titles: d.Titles,
		trimmer: d.Trimmer, surfaces: d.Surfaces, protocols: d.Protocols,
		httpClient: d.HTTPClient,
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
	mux.Handle("GET /v1/models",
		s.surface(messages, s.requireAPIKey(http.HandlerFunc(s.handleModels))))
	mux.Handle("POST /v1/messages",
		s.surface(messages, s.requireAPIKey(
			s.inference(messages, "/v1/messages", "/v1/messages?beta=true"))))

	// Serving count_tokens matters, and the client says so in its own
	// code: when a gateway answers 501 here, it falls back to measuring
	// the context by issuing a real max_tokens:1 inference request and
	// reading the usage off it. That is a billed request spent on
	// arithmetic, once per measurement. Removing this route because
	// "nothing seems to call it" would turn that on silently.
	mux.Handle("POST /v1/messages/count_tokens",
		s.surface(messages, s.requireAPIKey(
			s.inference(messages, "/v1/messages/count_tokens",
				"/v1/messages/count_tokens?beta=true"))))

	mux.Handle("POST /v1/responses",
		s.surface(responses, s.requireAPIKey(
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

	body, err := s.FetchModels(ctx)
	if err != nil {
		s.log.Warn("model discovery failed; answering 502 so the client keeps its cached list",
			"err", err, "request_id", httpx.RequestID(r.Context()))
		httpx.WriteError(w, http.StatusBadGateway, "api_error", "could not reach the upstream model list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
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
	accounts, err := s.store.ListAccounts(ctx)
	if err != nil {
		return nil, err
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
		if lastErr == nil {
			lastErr = pool.ErrNoAccounts
		}
		return nil, lastErr
	}
	return mergeModels(lists, serving), nil
}

// FetchModelsFor is the model list as one account sees it, unfiltered: the
// models its own subscription serves, which is what that account's switches
// are switches over. Subscriptions differ, so this is asked per account.
func (s *Gateway) FetchModelsFor(ctx context.Context, accountID, rawQuery string) ([]byte, error) {
	token, err := s.pool.AccessToken(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return s.listModels(ctx, token, rawQuery)
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
