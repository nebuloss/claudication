// Package gateway is the client-facing half of the HTTP surface: the APIs a
// client points at, with the authentication, limits and accounting around
// them. What a request then becomes upstream is the relay's business.
package gateway

import (
	"context"
	"encoding/json"
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
	"claudication/internal/service/install"
	"claudication/internal/service/limits"
	modelsvc "claudication/internal/service/models"
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
	store       Store
	keyLimiter  *limits.Limiter
	anonLimiter *limits.Limiter
	budgets     *limits.Budgets
	recorder    *usagesvc.Recorder
	pool        *pool.Pool
	upstreams   map[string]Upstream
	titles      *titles.Titler
	trimmer     *memlimit.Trimmer
	surfaces    *surfaces.Surfaces
	protocols   api.Registry
	install     *install.Links
	models      *modelsvc.Catalog
}

// Store is what the gateway reads and writes of the database on a request's
// path: the key a credential belongs to, its last use, and when its budget
// starts freeing up. Everything else it needs comes through the services it is
// handed, so the gateway cannot grow a query of its own unnoticed.
type Store interface {
	Authenticate(ctx context.Context, plaintext string) (store.APIKey, error)
	TouchKey(ctx context.Context, id string)
	OldestSpendAt(ctx context.Context, keyID string, since time.Time) (time.Time, bool)
}

// Deps is what a Gateway is built from. Every field is required.
type Deps struct {
	Config      *config.Config
	Log         *slog.Logger
	Store       Store
	KeyLimiter  *limits.Limiter
	AnonLimiter *limits.Limiter
	Budgets     *limits.Budgets
	Recorder    *usagesvc.Recorder
	Pool        *pool.Pool
	// Upstreams are the providers, by id, each with the relay that reaches
	// it and the client routes that may.
	Upstreams map[string]Upstream
	Titles    *titles.Titler
	Trimmer   *memlimit.Trimmer
	Surfaces  *surfaces.Surfaces
	Protocols api.Registry
	// Install keeps the one-time install bundles /install serves.
	Install *install.Links
	// Models is the model list /v1/models answers, and the aliases a request
	// may name.
	Models *modelsvc.Catalog
}

// Upstream is one provider as the gateway relays to it.
type Upstream struct {
	Relay *relay.Relay
	// Title names its accounts for a person: "Claude", "ChatGPT".
	Title string
	// Native is the client dialect it speaks itself (an api.Protocol's ID),
	// whose requests go to it as they came. A request in any other dialect
	// that reaches it is translated by that dialect's exchange.
	Native string
	// Paths is where each client route goes on it. A route missing here
	// cannot reach it: a request routed to it from there is refused, saying
	// which alias to change, rather than sent somewhere that cannot answer.
	Paths map[string]string
}

// New builds a Gateway.
func New(d Deps) *Gateway {
	return &Gateway{
		cfg: d.Config, log: d.Log, store: d.Store,
		keyLimiter: d.KeyLimiter, anonLimiter: d.AnonLimiter, budgets: d.Budgets,
		recorder: d.Recorder, pool: d.Pool, upstreams: d.Upstreams, titles: d.Titles,
		trimmer: d.Trimmer, surfaces: d.Surfaces, protocols: d.Protocols,
		install: d.Install, models: d.Models,
	}
}

// Routes registers the client-facing APIs on mux.
//
// Each is an api.Protocol and each is gated on its own switch, so one can be
// turned off without touching the other and both can be turned off at once.
//
// Which upstream a request reaches is decided per request, from the model it
// names (see inference); where on that upstream is the Upstream's Paths.
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
		s.surface(messages, s.requireAPIKey(messages, s.handleModels(messages))))
	mux.Handle("POST /v1/messages",
		s.surface(messages, s.requireAPIKey(messages, s.inference(messages, "/v1/messages"))))

	// Serving count_tokens matters, and the client says so in its own
	// code: when a gateway answers 501 here, it falls back to measuring
	// the context by issuing a real max_tokens:1 inference request and
	// reading the usage off it. That is a billed request spent on
	// arithmetic, once per measurement. Removing this route because
	// "nothing seems to call it" would turn that on silently.
	mux.Handle("POST /v1/messages/count_tokens",
		s.surface(messages, s.requireAPIKey(messages, s.inference(messages, "/v1/messages/count_tokens"))))

	mux.Handle("POST /v1/responses",
		s.surface(responses, s.requireAPIKey(responses, s.inference(responses, "/v1/responses"))))
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
//
// The 502 is in the dialect's own envelope, like every other refusal on /v1.
func (s *Gateway) handleModels(p api.Protocol) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Comfortably inside the client's 3-second budget.
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		// The list as this client is served, so a client that sizes its
		// requests from it sizes them for the model its own binding chose.
		key, _ := httpx.APIKey(r.Context())
		body, err := s.models.For(ctx, key, api.ClientName(r.UserAgent()))
		if err != nil {
			s.log.Warn("model discovery failed; answering 502 so the client keeps its cached list",
				"err", err, "request_id", httpx.RequestID(r.Context()))
			p.WriteError(w, http.StatusBadGateway, api.KindAPI, "could not reach the upstream model list")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(s.reachableFrom("/v1/messages", body))
	}
}

// reachableFrom is a model list less the models a client on route could not
// use: a model, or an alias serving one, whose provider that route cannot
// reach. A Claude client offered a GPT model would only meet it as a
// refusal. An entry is a provider's when it says so ("provider", which every
// provider but the default one's list sets); one that does not is the
// default's. A list it cannot read is returned as it was.
func (s *Gateway) reachableFrom(route string, body []byte) []byte {
	var list map[string]json.RawMessage
	if json.Unmarshal(body, &list) != nil {
		return body
	}
	var data []json.RawMessage
	if json.Unmarshal(list["data"], &data) != nil {
		return body
	}
	kept := make([]json.RawMessage, 0, len(data))
	var ids []string
	for _, e := range data {
		var m struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
		}
		if json.Unmarshal(e, &m) == nil && m.Provider != "" {
			if _, ok := s.upstreams[m.Provider].Paths[route]; !ok {
				continue
			}
		}
		kept = append(kept, e)
		ids = append(ids, m.ID)
	}
	if len(kept) == len(data) {
		return body
	}
	set := func(field string, v any) {
		if raw, err := json.Marshal(v); err == nil {
			list[field] = raw
		}
	}
	set("data", kept)
	// The paging fields name what is in it now.
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
			p.WriteError(w, http.StatusNotFound, api.KindNotFound,
				"the "+p.Title()+" is turned off on this gateway; "+
					"an administrator can switch it back on under Settings")
			return
		}
		next.ServeHTTP(w, r)
	})
}
