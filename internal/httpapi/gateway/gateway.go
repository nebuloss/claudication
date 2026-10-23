// Package gateway is the client-facing half of the HTTP surface: the APIs a
// client points at, with the authentication, limits and accounting around
// them. What a request then becomes upstream is the relay's business.
package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"claudication/internal/api"
	anthropicapi "claudication/internal/api/anthropic"
	geminiapi "claudication/internal/api/gemini"
	"claudication/internal/api/openai"
	"claudication/internal/config"
	"claudication/internal/httpapi/httpx"
	"claudication/internal/memlimit"
	"claudication/internal/pool"
	"claudication/internal/relay"
	"claudication/internal/service/install"
	"claudication/internal/service/limits"
	modelsvc "claudication/internal/service/models"
	"claudication/internal/service/settings"
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
	inflight    *limits.InFlight
	recorder    *usagesvc.Recorder
	pool        *pool.Pool
	upstreams   map[string]Upstream
	titles      *titles.Titler
	trimmer     *memlimit.Trimmer
	enabled     *settings.Switch
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
	MarkConfigured(ctx context.Context, keyID string) error
	SetClientBase(ctx context.Context, keyID, base string) error
}

// Deps is what a Gateway is built from. Every field is required.
type Deps struct {
	Config      *config.Config
	Log         *slog.Logger
	Store       Store
	KeyLimiter  *limits.Limiter
	AnonLimiter *limits.Limiter
	Budgets     *limits.Budgets
	// InFlight counts each key's open requests, for the client's Limits page.
	InFlight *limits.InFlight
	Recorder *usagesvc.Recorder
	Pool     *pool.Pool
	// Upstreams are the providers, by id, each with the relay that reaches
	// it and the client routes that may.
	Upstreams map[string]Upstream
	Titles    *titles.Titler
	Trimmer   *memlimit.Trimmer
	// Enabled is the gateway switch: off, every client-facing route refuses.
	Enabled   *settings.Switch
	Protocols api.Registry
	// Install keeps the one-time install bundles /install serves.
	Install *install.Links
	// Models is the model list /v1/models answers, and what a client's model
	// names resolve to.
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
	// which name to rebind, rather than sent somewhere that cannot answer.
	Paths map[string]string
	// FitOutput lowers a body's output budget, in this upstream's own field,
	// to limit; ok is false when it did not need to. Run on a request that
	// named a slot (internal/relay/passes, FitMaxTokens); nil fits nothing.
	FitOutput func(body []byte, limit int) (out []byte, ok bool)
}

// New builds a Gateway.
func New(d Deps) *Gateway {
	return &Gateway{
		cfg: d.Config, log: d.Log, store: d.Store,
		keyLimiter: d.KeyLimiter, anonLimiter: d.AnonLimiter, budgets: d.Budgets, inflight: d.InFlight,
		recorder: d.Recorder, pool: d.Pool, upstreams: d.Upstreams, titles: d.Titles,
		trimmer: d.Trimmer, enabled: d.Enabled, protocols: d.Protocols,
		install: d.Install, models: d.Models,
	}
}

// Routes registers the client-facing APIs on mux.
//
// Each is an api.Protocol, and every one is gated on the gateway switch.
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
		s.serving(messages, s.requireAPIKey(messages, s.handleModels(messages))))
	mux.Handle("POST /v1/messages",
		s.serving(messages, s.requireAPIKey(messages, s.inference(messages, "/v1/messages"))))

	// Serving count_tokens matters, and the client says so in its own
	// code: when a gateway answers 501 here, it falls back to measuring
	// the context by issuing a real max_tokens:1 inference request and
	// reading the usage off it. That is a billed request spent on
	// arithmetic, once per measurement. Removing this route because
	// "nothing seems to call it" would turn that on silently.
	mux.Handle("POST /v1/messages/count_tokens",
		s.serving(messages, s.requireAPIKey(messages, s.inference(messages, "/v1/messages/count_tokens"))))

	mux.Handle("POST /v1/responses",
		s.serving(responses, s.requireAPIKey(responses, s.inference(responses, "/v1/responses"))))

	// The Gemini API, which gemini-cli speaks pointed at a gateway: the
	// model and the method in one path segment (gemini.go).
	if gemini, ok := s.protocols.Find(geminiapi.ID); ok {
		mux.Handle("POST /v1beta/models/{call}",
			s.serving(gemini, s.requireAPIKey(gemini, s.geminiCall(gemini))))
	}
}

// Guard is what fronts every client-facing route, for one outside this
// package: the gateway switch, then the key — authenticated, held to its rate
// limit and budget, its client refused when switched off — exactly as on /v1,
// each refusal in p's envelope. The MCP server (internal/httpapi/agent) is
// such a route: the client's own key reaches it, and a key /v1 would refuse
// is refused there too. Handed across by the composition root, since the
// surfaces do not import one another.
func (s *Gateway) Guard(p api.ErrorWriter, next http.Handler) http.Handler {
	return s.serving(p, s.requireAPIKey(p, next))
}

// handleModels answers the discovery request by relaying Anthropic's own model
// list, so it stays correct as models come and go.
//
// The contract pins the shape hard: Claude Code sends GET /v1/models?limit=1000
// with a 3-second timeout, treats any redirect as failure, and keeps only ids
// containing "claude" or "anthropic" — though 2.1.304 asks only with
// CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY set, which the files the gateway
// writes do not set, so a typed Claude Code client's plain names are never
// filtered here (internal/service/clients, ClaudeCode).
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
		body, err := s.models.For(ctx, key)
		if err != nil {
			s.log.Warn("model discovery failed; answering 502 so the client keeps its cached list",
				"err", err, "request_id", httpx.RequestID(r.Context()))
			p.WriteError(w, http.StatusBadGateway, api.KindAPI, "could not reach the upstream model list")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}
}

// Setting is the settings row holding the gateway switch (see serving). It
// replaced one row per API at schema 032, which set it off only where both
// had been.
const Setting = "gateway.enabled"

// serving gates a route on the gateway being switched on.
//
// One switch for every client-facing route. There were two, one per API; with
// each client pointed at the API its type speaks, switching one API off was
// only ever a way to break some clients and not others, and the switch an
// operator reaches for at three in the morning is "stop serving".
//
// It answers 404 rather than 503 on purpose. 503 says "try again shortly" and
// a client obediently will, for as long as the gateway stays off; 404 says
// the gateway does not serve this, which is the truth and which stops the
// client rather than making it spin. The message says where to turn it back
// on, because the operator reading it in a client's log is usually not the
// one who flipped the switch. It is in the caller's dialect, as every refusal
// on /v1 is.
//
// The check is per request and reads an atomic, so the switch takes effect on
// the next request with no restart and no lock on the hot path. The admin UI
// is not behind it, which is how it gets switched back on.
func (s *Gateway) serving(p api.ErrorWriter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.enabled.On() {
			p.WriteError(w, http.StatusNotFound, api.KindNotFound,
				"this gateway is switched off; an administrator can switch it back on under Settings")
			return
		}
		next.ServeHTTP(w, r)
	})
}
