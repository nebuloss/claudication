// Package httpapi is where the gateway's HTTP surface is put together.
//
// It is the composition root and nothing else: it builds the services, hands
// each handler set what it needs, and decides which listener serves which.
// The handlers themselves live one level down, one package per surface:
//
//	httpx    the plumbing all of them share: errors, context, middleware
//	gateway  the client-facing APIs, behind an API key
//	admin    the admin API, behind a session
//	web      the embedded UI, and the welcome page at a relay's root
//
// None of them imports another. What one needs from another — the model list,
// the sign-in link at the root — is handed across here, as a function.
package httpapi

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"claudication/internal/api"
	anthropicapi "claudication/internal/api/anthropic"
	"claudication/internal/api/openai"
	"claudication/internal/config"
	"claudication/internal/handover"
	"claudication/internal/httpapi/admin"
	"claudication/internal/httpapi/gateway"
	"claudication/internal/httpapi/httpx"
	"claudication/internal/httpapi/web"
	"claudication/internal/memlimit"
	"claudication/internal/oauth"
	"claudication/internal/pool"
	"claudication/internal/provider"
	"claudication/internal/provider/anthropic"
	chatgpt "claudication/internal/provider/openai"
	"claudication/internal/relay"
	"claudication/internal/relay/passes"
	accountsvc "claudication/internal/service/accounts"
	"claudication/internal/service/install"
	"claudication/internal/service/limits"
	modelsvc "claudication/internal/service/models"
	"claudication/internal/service/settings"
	"claudication/internal/service/titles"
	"claudication/internal/service/uptime"
	usagesvc "claudication/internal/service/usage"
	"claudication/internal/store"
	"claudication/internal/version"
)

// webdist holds the built admin UI. `make web` populates it; the .gitkeep
// placeholder keeps this directive valid in a source-only checkout, so the Go
// build never depends on Node having run.
//
// Embedded here rather than in package web because go:embed reaches only
// below the file that names it, and the build writes to this directory.
//
//go:embed all:webdist
var webdist embed.FS

type Server struct {
	cfg         *config.Config
	log         *slog.Logger
	store       *store.Store
	keyLimiter  *limits.Limiter
	anonLimiter *limits.Limiter
	budgets     *limits.Budgets
	// inflight counts each key's open requests: the gateway counts, the
	// admin API reads.
	inflight       *limits.InFlight
	recorder       *usagesvc.Recorder
	trustedProxies []*net.IPNet
	adminAddr      string
	stopSweeper    chan struct{}
	pool           *pool.Pool
	// relay is Anthropic's, and relays every provider's by id; see
	// buildServices.
	relay      *relay.Relay
	relays     map[string]*relay.Relay
	httpClient *http.Client
	// poller keeps each account's subscription usage current, faster while
	// the admin UI is open. See internal/service/accounts.
	poller *accountsvc.Poller
	// trimmer counts relayed requests in flight and hands idle heap back to
	// the system once a burst is over. See internal/memlimit.
	trimmer *memlimit.Trimmer
	// protocols are the client-facing dialects this gateway serves, in the
	// order the admin UI lists them. Anthropic is one of them rather than the
	// default case — see internal/api.
	protocols api.Registry
	// providers are the upstreams accounts connect to, in the order the admin
	// UI lists them. See internal/provider.
	providers provider.Registry
	// serving is the gateway switch: off, no client-facing route answers.
	serving *settings.Switch

	// uptime samples the gateway's own state once a minute, for the status
	// page and the Overview. See internal/service/uptime.
	uptime *uptime.Sampler

	// models is the model list, which all three surfaces read.
	models *modelsvc.Catalog

	// The handler sets, one per surface. See the package comment.
	gateway *gateway.Gateway
	admin   *admin.Admin
	web     *web.Site

	// listeners are the addresses served, the relay's first; adminListener is
	// the admin's own, when admin-listen gives it one.
	listeners     []*listener
	adminListener *listener

	// reloadable is whether this process was handed its sockets by a
	// supervisor, so a reload replaces it without refusing a connection.
	// /health says so, which is how the installer tells a running gateway it
	// can hand over to from one it has to stop (scripts/install.sh).
	reloadable bool
	// instance tells this process from any other serving the same version:
	// a handover to the same release is proven by it changing, which the
	// version string cannot show (scripts/install.sh).
	instance string

	mu   sync.Mutex
	addr string
}

// Addr reports the bound address once Run has started listening. Empty before
// then. Useful for tests and for logging when the port is ephemeral.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// AdminAddr reports the admin listener's address, or empty when the admin
// surface shares the main one.
func (s *Server) AdminAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.adminAddr
}

// servingWhat names what the main listener carries, so the startup line says
// whether the admin surface is on it.
func servingWhat(combined bool) string {
	if combined {
		return "relay, admin API and UI"
	}
	return "relay only"
}

func New(cfg config.Config, log *slog.Logger, st *store.Store) (*Server, error) {
	trusted, err := httpx.ParseTrustedProxies(cfg.TrustedProxies)
	if err != nil {
		return nil, fmt.Errorf("trusted-proxies: %w", err)
	}

	s := &Server{
		cfg:            &cfg,
		log:            log,
		store:          st,
		keyLimiter:     limits.NewLimiter(),
		inflight:       limits.NewInFlight(),
		anonLimiter:    limits.NewLimiter(),
		budgets:        limits.NewBudgets(st),
		trustedProxies: trusted,
		stopSweeper:    make(chan struct{}),
		trimmer:        memlimit.NewTrimmer(log),
		instance:       newInstanceID(),
		// Upstream calls made by the gateway itself: token exchange, refresh,
		// credential probes. Short timeout, because these are all small.
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}

	// Every stored switch is read once here rather than per request, with a
	// bound on how long startup may wait for the database to answer.
	loadCtx, cancelLoad := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelLoad()

	s.loadSwitches(loadCtx)
	titler := s.buildServices(loadCtx)
	s.buildHandlers(titler)
	s.buildListeners()
	return s, nil
}

// loadSwitches registers the client dialects and reads every runtime switch.
// A switch that cannot be read is said out loud and falls back to its default;
// none of them is worth refusing to start over.
func (s *Server) loadSwitches(ctx context.Context) {
	// The composition root: the one place that knows which dialects exist.
	// Order is what the admin UI lists.
	s.protocols = api.Registry{
		anthropicapi.New(),
		openai.New(),
	}
	// On unless an operator switched it off: a switch that cannot be read
	// leaves the gateway serving, which is the state it was in before there
	// was one.
	s.serving = settings.NewSwitch(s.store, gateway.Setting, true)
	if err := settings.Load(ctx, s.store, s.serving); err != nil {
		s.log.Warn("could not read the gateway switch; serving", "err", err)
	}
}

// buildServices builds what the handlers share: the usage recorder, the
// account pool and its poller, the relay, and the titler, which is returned
// because only the handlers built from it hold it.
func (s *Server) buildServices(ctx context.Context) *titles.Titler {
	cfg, st, log := s.cfg, s.store, s.log

	// The providers are named here, in the composition root, and nowhere
	// below: the pool is handed each one's token refresh, the poller each
	// one's usage endpoint, the recorder each one's error vocabulary and the
	// relay its wire, and none of them knows whose they are.
	s.providers = provider.Registry{anthropic.Accounts{}, chatgpt.Accounts{}}

	// What each request cost, classified in the vocabulary of the provider
	// that answered and credited to the key's budget. A request no account
	// served is in the first provider's, which is what the gateway's own
	// refusals were always classified in.
	s.recorder = &usagesvc.Recorder{
		Store:     st,
		Retention: cfg.Usage.Retention(),
		Classify: func(providerID string, status int, text string) string {
			p, ok := s.providers.Find(providerID)
			if !ok {
				p = s.providers[0]
			}
			return p.ErrorCode(status, text)
		},
		Budgets: s.budgets,
		Log:     log,
	}
	refresh := map[string]pool.RefreshFunc{}
	for _, p := range s.providers {
		refresh[p.ID()] = p.Refresh
	}
	s.pool = pool.New(st, s.httpClient, log, refresh)
	// Across processes as well as within this one: see LockRefreshes.
	s.pool.LockRefreshes(handover.FileLock(filepath.Join(cfg.StateDir, "locks"), log))
	// A ChatGPT conversation stays on the account that served it last: its
	// encrypted reasoning was made there, and whether another account can
	// read it is not measured. See internal/pool, affinity.go.
	s.pool.PreferLastAccount(chatgpt.ID)
	s.poller = &accountsvc.Poller{
		Store:  st,
		Tokens: s.pool,
		Fetch: func(ctx context.Context, a store.Account, token string) (store.AccountQuota, error) {
			p, ok := s.providers.Find(a.Provider)
			if !ok {
				return store.AccountQuota{}, fmt.Errorf("no provider %q", a.Provider)
			}
			q, err := p.Usage(ctx, s.httpClient, provider.Credential{Token: token, AccountID: a.AccountUUID})
			if err != nil {
				return store.AccountQuota{}, err
			}
			return pool.StoredQuota(q), nil
		},
		Idle:    cfg.Usage.PollIdle.D(),
		Watched: cfg.Usage.PollWatched.D(),
		Log:     log,
	}
	// One relay per provider, each with its own wire and passes, and one
	// connection pool between them.
	relayClient := newRelayClient()
	s.relay = &relay.Relay{
		Wire: anthropic.Provider{},
		Pool: s.pool,
		Log:  log,
		Passes: passes.Default(passes.Options{
			Attribution: cfg.Passthrough.ClaudeCodeAttribution,
			Images:      passes.NewImageCache(passes.DefaultImageCacheBytes),
			Log:         log,
		}),
		StallTimeout: cfg.Passthrough.StallTimeout.D(),
		Client:       relayClient,
	}
	openaiRelay := &relay.Relay{
		Wire:   chatgpt.Provider{},
		Pool:   s.pool,
		Log:    log,
		Passes: passes.OpenAI(passes.Options{Log: log}),
		// No stall hold. It was measured on Anthropic's streams, which carry
		// thinking as deltas; a ChatGPT reasoning model with summaries off —
		// the backend's default — sends nothing until its reasoning is done,
		// and a hold would resend a turn that was only thinking.
		StallTimeout: 0,
		Client:       relayClient,
	}
	s.relays = map[string]*relay.Relay{anthropic.ID: s.relay, chatgpt.ID: openaiRelay}

	s.uptime = &uptime.Sampler{
		Store:     st,
		Probe:     s.uptimeState,
		Retention: cfg.Usage.Retention(),
		Log:       log,
	}

	// Chat titles, once the relays and the recorder they use exist: a title
	// is asked of the provider that served the chat, at the path the
	// gateway's own routes send that provider a client's turn.
	ups := s.upstreams()
	return titles.New(titles.Deps{
		Store: st,
		Upstreams: map[string]titles.Upstream{
			anthropic.ID: {Relay: s.relay, Path: ups[anthropic.ID].Paths["/v1/messages"]},
			chatgpt.ID:   {Relay: openaiRelay, Path: ups[chatgpt.ID].Paths["/v1/responses"], Responses: true},
		},
		Record: s.recorder.Record,
		Log:    log,
	})
}

// newRelayClient is the client relayed inference goes out on.
//
// Its own, with NO client-level timeout: a streaming response legitimately
// runs for many minutes, and a Timeout here would sever it mid-flight. The
// per-request context carries the real deadline instead.
func newRelayClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConnsPerHost:   32,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
			ExpectContinueTimeout: time.Second,
			// Streaming responses must not be buffered by the transport.
			ForceAttemptHTTP2: true,
			// The relay sets Accept-Encoding: identity itself, so there is
			// nothing here for the transport to negotiate or unwrap. Saying
			// so explicitly keeps the two from drifting apart: it is the
			// transport's *implicit* gzip, which only decompresses when it
			// also set the header, that made a client's own Accept-Encoding
			// silently break usage accounting.
			DisableCompression: true,
		},
	}
}

// buildHandlers builds the handler sets. Each is handed what it uses and
// nothing else; what one needs from another — the model list — crosses here
// as a function.
func (s *Server) buildHandlers(titler *titles.Titler) {
	cfg, st, log := s.cfg, s.store, s.log

	// Install links live a quarter of an hour: long enough to copy a command
	// to another machine, short enough that one never run is soon gone.
	installs := install.New(15 * time.Minute)
	s.models = modelsvc.New(modelsvc.Deps{
		Config: cfg, Log: log, Store: st, Pool: s.pool,
		Relays:     []*relay.Relay{s.relays[anthropic.ID], s.relays[chatgpt.ID]},
		HTTPClient: s.httpClient,
	})
	s.gateway = gateway.New(gateway.Deps{
		Config: cfg, Log: log, Store: st,
		KeyLimiter: s.keyLimiter, AnonLimiter: s.anonLimiter, Budgets: s.budgets,
		Recorder: s.recorder, Pool: s.pool, Upstreams: s.upstreams(), Titles: titler,
		Trimmer: s.trimmer, Enabled: s.serving, Protocols: s.protocols, InFlight: s.inflight,
		Install: installs, Models: s.models,
	})
	s.admin = admin.New(admin.Deps{
		Config: cfg, Log: log, Store: st, Pool: s.pool,
		Pending: oauth.NewPending(15 * time.Minute), Poller: s.poller,
		Serving: s.serving, InFlight: s.inflight,
		Protocols: s.protocols, HTTPClient: s.httpClient, Providers: s.providers,
		AnonLimiter: s.anonLimiter, Budgets: s.budgets,
		TrustedProxies: s.trustedProxies, StartedAt: time.Now(),
		Models: s.models, Install: installs,
	})
	s.web = web.New(web.Deps{Log: log, Files: webdist})
}

// upstreams are the providers as the gateway reaches them: which client
// routes may, and where each goes.
//
//	                  Anthropic                  ChatGPT
//	/v1/messages      /v1/messages?beta=true     /responses (translated)
//	…/count_tokens    …/count_tokens?beta=true   answered here (estimated)
//	/v1/responses     /v1/messages?beta=true     /responses
//	                  (translated)               (as it came)
//
// The ?beta=true is the official client's own. Each API reaches the other's
// provider translated: Responses into Messages for Claude
// (internal/api/openai), Messages into Responses for ChatGPT
// (internal/api/anthropic). count_tokens has no path on ChatGPT because the
// backend counts nothing; the translation answers it with an estimate that
// says it is one, and the gateway never relays it.
func (s *Server) upstreams() map[string]gateway.Upstream {
	return map[string]gateway.Upstream{
		anthropic.ID: {
			Relay: s.relays[anthropic.ID], Title: "Claude", Native: anthropicapi.ID,
			Paths: map[string]string{
				"/v1/messages":              "/v1/messages?beta=true",
				"/v1/messages/count_tokens": "/v1/messages/count_tokens?beta=true",
				"/v1/responses":             "/v1/messages?beta=true",
			},
		},
		chatgpt.ID: {
			Relay: s.relays[chatgpt.ID], Title: "ChatGPT", Native: openai.ID,
			Paths: map[string]string{
				"/v1/messages":  "/responses",
				"/v1/responses": "/responses",
			},
		},
	}
}

// listener is one address the gateway serves, what it serves there, and the
// line it logs when it starts.
type listener struct {
	// key is the config key the address came from, ListenRelay or
	// ListenAdmin: the name an inherited socket is handed down under.
	key   string
	addr  string
	srv   *http.Server
	msg   string
	attrs []any
	ln    net.Listener
	// conns follows srv's connections, for the drain; see handover.Conns.
	conns *handover.Conns
}

// The config keys of the two addresses, and the names their sockets are
// handed down under (internal/handover).
const (
	ListenRelay = "listen"
	ListenAdmin = "admin-listen"
)

func (s *Server) newListener(key, addr string, h http.Handler, msg string, attrs ...any) *listener {
	l := &listener{
		key:  key,
		addr: addr,
		srv: &http.Server{
			Addr:    addr,
			Handler: h,
			// No WriteTimeout: responses are long-lived SSE streams and a write
			// deadline would sever them mid-flight. ReadHeaderTimeout still
			// protects against slowloris on the request side.
			ReadHeaderTimeout: 30 * time.Second,
			IdleTimeout:       120 * time.Second,
			ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
		},
		msg:   msg,
		attrs: attrs,
	}
	l.conns = handover.Track(l.srv)
	return l
}

// buildListeners decides which listener serves which surface. The relay's is
// always first: its address is Addr, and its error is the one reported.
func (s *Server) buildListeners() {
	cfg := s.cfg
	// With admin-listen set, the relay's listener drops the admin API and the
	// UI; they move to a listener of their own. Unset, it keeps serving both.
	split := cfg.AdminListen != ""
	s.listeners = []*listener{s.newListener(ListenRelay, cfg.Listen,
		s.routes(role{gateway: true, admin: !split}),
		"listening", "serving", servingWhat(!split),
		"version", version.Version, "state_dir", cfg.StateDir)}
	if split {
		s.adminListener = s.newListener(ListenAdmin, cfg.AdminListen, s.routes(role{admin: true}),
			"admin listening", "serving", "admin API and UI")
		s.listeners = append(s.listeners, s.adminListener)
	}
}

// role says which surfaces a listener serves.
//
// Both on one listener is the default and is right on a machine only the
// operator can reach. Split, the relay can be published while the admin API
// and UI stay on an address that is not — a separation the gateway enforces
// itself rather than trusting a proxy to.
//
// The root follows from it: the admin UI wherever the admin is served, and
// the welcome page on a listener that relays only.
type role struct {
	gateway bool
	admin   bool
}

// setDocumentHeaders guards the two pages this gateway serves.
//
// no-referrer so a ?token= link cannot leak to Anthropic when the consent tab
// opens; nosniff because we serve JavaScript from the same origin as
// user-supplied account data.
func setDocumentHeaders(w http.ResponseWriter) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func (s *Server) routes(r0 role) http.Handler {
	mux := http.NewServeMux()

	// Unauthenticated: liveness only, with no information about accounts,
	// models or configuration. On every listener, because whatever watches
	// one of them needs something to watch.
	mux.HandleFunc("GET /health", s.handleHealth)
	// The status page's data: availability over time, aggregated, and
	// nothing else (see internal/service/uptime and docs/status-page.md).
	// On every listener for the same reason as /health, and because the
	// admin Overview draws the same history from it.
	mux.HandleFunc("GET /status.json", s.handleStatus)

	// Claude Code sends a best-effort connection-warming probe here. The
	// gateway contract says it may be rejected, but answering it costs
	// nothing and saves a confusing 404 in the logs.
	mux.HandleFunc("HEAD /api/hello", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	if r0.gateway {
		s.gateway.Routes(mux)
	}
	if r0.admin {
		s.admin.Routes(mux)
	}

	// Anything under an API prefix that did not match above is a client error,
	// and it has to say so in the client's own language. Without these, an
	// unknown /v1/... path would fall through to the UI's catch-all and answer
	// a web page, which parses as neither JSON nor an explanation.
	for _, prefix := range []string{"/v1/", "/admin/", "/api/"} {
		mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
			httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such endpoint: "+r.URL.Path)
		})
	}

	// The admin UI, built by Vite and embedded in the binary, served at the
	// origin root: typing the host opens it. The API keeps its own prefixes, so
	// this catch-all can only ever reach a path none of them claimed.
	//
	// A ?token= magic link is consumed here so the credential is exchanged for
	// a cookie before any asset is served.
	//
	// Registered without a method on purpose: "GET /" and "/v1/" are neither
	// more specific than the other, which is precisely what ServeMux panics on.
	// A method-less "/" is the most general pattern there is, so every API
	// prefix above is a strict subset of it and nothing conflicts.
	//
	// Where the admin UI is not served — the relay's listener once admin-listen
	// splits it off — the root is the welcome page instead: the one path a
	// person reaches by typing rather than by calling, so it answers in HTML
	// and says what the address is. Every other path there is the API's.
	page := s.web.WelcomeHandler()
	if r0.admin {
		page = s.web.StaticHandler("index.html")
	}
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Everything below serves documents, so nothing below answers a write.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such endpoint: "+r.URL.Path)
			return
		}
		setDocumentHeaders(w)
		// Only a page load spends the link, and only where there is an admin
		// to sign in to. Every request under this handler carries the query
		// string it was reached with, so a prefetched favicon, a stylesheet,
		// or a service worker fetching `/?token=…` would each burn a
		// single-use sign-in link and leave the operator looking at "already
		// used" on the request they actually made.
		if r0.admin && httpx.IsNavigation(r) && s.admin.TokenLogin(w, r, "/") {
			return
		}
		page.ServeHTTP(w, r)
	}))

	var h http.Handler = mux
	h = httpx.WithBodyLimit(h, s.cfg.Limits.MaxBodyBytes)
	h = httpx.WithRecovery(h, s.log)
	h = httpx.WithAccessLog(h, s.log)
	h = httpx.WithRequestContext(h, s.trustedProxies)
	return h
}

// handleHealth says the process is up, its build, and whether it is
// accepting client requests: the Gateway switch, which the welcome page shows
// and a monitor can alert on. Up and not accepting is still status ok — the
// process is healthy, its operator switched it off.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":    "ok",
		"version":   version.Version,
		"accepting": s.serving.On(),
		// A binary from before the handover says nothing here, which the
		// installer reads the same as false.
		"reloadable": s.reloadable,
		"instance":   s.instance,
	})
}

// newInstanceID is random, so it says nothing about the process — not its
// pid, not when it started — beyond being a different one.
func newInstanceID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// uptimeState is the gateway's state for the uptime record: paused when its
// operator switched it off, up when some account of some provider could take
// a request now, degraded when none could.
//
// "Could take a request" is the pool's Usable — not disabled, not cooling,
// not out of room — rather than Serving, which names an account whenever
// there is one at all, cooling or spent, because pick() would rather try a
// doubtful account than refuse. That is right for a request and wrong for a
// status: every account sidelined is exactly what degraded means.
func (s *Server) uptimeState(ctx context.Context) uptime.State {
	if !s.serving.On() {
		return uptime.Paused
	}
	for _, p := range s.providers {
		st, err := s.pool.Status(ctx, p.ID())
		if err == nil && st.Usable > 0 {
			return uptime.Up
		}
	}
	return uptime.Degraded
}

// handleStatus answers the availability report. Unauthenticated, so it is
// limited per address like every anonymous route, and cached: the report is
// rebuilt at most once a minute however often it is asked, and says so to
// any cache in front.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !s.anonLimiter.AllowPerMinute("status:"+httpx.ClientIP(r.Context()), s.cfg.Limits.AnonPerMinute) {
		httpx.WriteError(w, http.StatusTooManyRequests, httpx.KindRateLimit, "too many requests; try again in a minute")
		return
	}
	rep, err := s.uptime.Report(r.Context())
	if err != nil {
		s.log.Warn("could not build the status report", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "status unavailable")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	httpx.WriteJSON(w, http.StatusOK, rep)
}

// Run serves until ctx is cancelled, then drains within the configured grace
// period.
//
// Both SIGINT and SIGTERM reach this through ctx. auth2api handles only
// SIGINT, so every `docker stop` and `systemctl stop` kills it before its
// buffers flush and severs in-flight streams mid-frame.
func (s *Server) Run(ctx context.Context) error {
	return s.Serve(ctx, Handoff{})
}

// Handoff is what a worker under a supervisor is given (internal/handover):
// sockets already bound, someone to tell when it is serving, and the word to
// let go.
type Handoff struct {
	// Listeners are the inherited sockets, by config key (ListenRelay,
	// ListenAdmin). Non-nil, every listener must be among them: a worker
	// that bound one of its own would hold a port the supervisor does not,
	// and the next handover would find it taken.
	Listeners map[string]net.Listener
	// Ready is called once every listener is being served.
	Ready func()
	// Release, closed, is a handover: stop accepting and drain, as a stop
	// does, but record no outage — the sockets stay open in the supervisor
	// and the worker taking over, so nothing is refused.
	Release <-chan struct{}
}

// ErrListenersChanged is a worker whose config names other listeners than the
// sockets it inherited. The supervisor and the worker read the config each on
// their own, so an edit between the two can do this; serving anyway would
// leave an inherited socket that nobody accepts on, its connections waiting
// in the queue for ever, or a listener with no socket at all.
var ErrListenersChanged = errors.New("the config names other listeners than the inherited sockets")

func (s *Server) matchInherited(inherited map[string]net.Listener) error {
	want := map[string]bool{}
	for _, l := range s.listeners {
		want[l.key] = true
	}
	same := len(want) == len(inherited)
	for k := range inherited {
		same = same && want[k]
	}
	if same {
		return nil
	}
	have := make([]string, 0, len(inherited))
	for k := range inherited {
		have = append(have, k)
	}
	slices.Sort(have)
	names := make([]string, 0, len(s.listeners))
	for _, l := range s.listeners {
		names = append(names, l.key)
	}
	return fmt.Errorf("%w: inherited %v, config names %v; restart to rebind", ErrListenersChanged, have, names)
}

// Serve is Run, with the sockets and the signals of a handover.
func (s *Server) Serve(ctx context.Context, h Handoff) error {
	if h.Listeners != nil {
		if err := s.matchInherited(h.Listeners); err != nil {
			return err
		}
	}
	// Every listener binds before anything is served, so a port clash on the
	// admin address is a startup failure rather than a gateway that
	// comes up with no way in.
	for i, l := range s.listeners {
		var ln net.Listener
		var err error
		if h.Listeners != nil {
			ln = h.Listeners[l.key] // there: matchInherited
		} else {
			ln, err = net.Listen("tcp", l.addr)
		}
		if err != nil {
			for _, bound := range s.listeners[:i] {
				_ = bound.ln.Close()
			}
			return fmt.Errorf("listen on %s: %w", l.addr, err)
		}
		l.ln = ln
	}
	s.reloadable = h.Release != nil
	s.mu.Lock()
	s.addr = s.listeners[0].ln.Addr().String()
	if s.adminListener != nil {
		s.adminAddr = s.adminListener.ln.Addr().String()
	}
	s.mu.Unlock()

	// The background work starts only once every listener is bound. Started
	// before, a port clash on the admin address returned with the
	// sweepers, the pruner and the poller still running against the database —
	// harmless in a process about to exit, and a test's state directory being
	// written to while it was removed (TestRunFailsOnAListenerClash, about one
	// run in five under load).
	go s.keyLimiter.RunSweeper(s.stopSweeper, time.Minute, 10*time.Minute)
	go s.anonLimiter.RunSweeper(s.stopSweeper, time.Minute, 10*time.Minute)
	go s.budgets.RunSweeper(s.stopSweeper, 10*time.Minute, time.Hour)
	go s.recorder.RunPruner(s.stopSweeper, 24*time.Hour)
	// The poller stops as soon as this process stops taking requests, on a
	// handover as on a stop: two processes polling the same accounts would
	// be twice the calls for nothing.
	pollCtx, stopPolling := context.WithCancel(ctx)
	defer stopPolling()
	go s.poller.Run(pollCtx, s.stopSweeper, accountsvc.Tick)
	go s.trimmer.Run(s.stopSweeper, 5*time.Second)
	// Every listener is bound by now: the stop the last run recorded, if it
	// recorded one, ends here. After a handover there is none to end.
	s.uptime.Started(ctx)
	go s.uptime.Run(s.stopSweeper)

	// Buffered for every listener, so no goroutine blocks on a send once
	// another has already reported and the select has moved on.
	errCh := make(chan error, len(s.listeners))
	for _, l := range s.listeners {
		go func() {
			s.log.Info(l.msg, append([]any{"addr", l.ln.Addr().String()}, l.attrs...)...)
			if err := l.srv.Serve(l.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
				return
			}
			errCh <- nil
		}()
	}

	if h.Ready != nil {
		h.Ready()
	}

	stopBackground := sync.OnceFunc(func() {
		stopPolling()
		close(s.stopSweeper)
	})
	released := false
	select {
	case err := <-errCh:
		stopBackground()
		return err
	case <-ctx.Done():
	case <-h.Release:
		released = true
	}
	// Every background writer stops here, not after the drain: the sampler,
	// the pruners, the sweepers and the poller belong to the process taking
	// requests, and on a handover that is the other one now. What is left is
	// answering what was accepted, and recording what it cost.
	stopBackground()

	grace := s.cfg.Shutdown.Grace.D()
	if released {
		// No outage: the sockets are open in the process taking over, which
		// is accepting already. Only what this one is answering is left.
		s.log.Info("handed over; draining", "grace", grace.String())
	} else {
		s.log.Info("shutting down", "grace", grace.String())
		// Recorded before the listeners close: from here a new request is
		// refused, however long the drain then takes, and the status page
		// must count it. Not under ctx, which is already done.
		s.uptime.Stopping(context.Background())
	}

	// Stop accepting first, and wait for the accept loops to return, so the
	// connections counted below are all there will be; each reports the
	// closed listener, which is no error here.
	for _, l := range s.listeners {
		if !released {
			l.srv.SetKeepAlivesEnabled(false)
		}
		_ = l.ln.Close()
	}
	for range s.listeners {
		<-errCh
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if released {
		// A handover closes no connection a client could be writing on: each
		// idle keep-alive connection answers its next request with
		// Connection: close, and the drain waits for every one to be closed
		// (handover.Conns). Closing them at once, as Shutdown does, failed
		// POSTs written on a pooled connection at that instant.
		var wg sync.WaitGroup
		for _, l := range s.listeners {
			wg.Add(1)
			go func() {
				defer wg.Done()
				l.conns.Drain(shutdownCtx)
			}()
		}
		wg.Wait()
	} else {
		// A stop lets every connection already accepted reach a handler
		// before Shutdown, which drops a request it has not yet handed to one
		// (handover.Conns.Settle). Bounded, because a client that connects
		// and sends nothing would hold it: five seconds is how long Shutdown
		// itself waits on such a one.
		settleCtx, settled := context.WithTimeout(shutdownCtx, 5*time.Second)
		for _, l := range s.listeners {
			l.conns.Settle(settleCtx)
		}
		settled()
	}

	// All of them at once. Shutdown closes a server's listeners only when it is
	// called, so in sequence the admin port stayed bound for as long as the
	// relay took to drain — up to the whole grace period behind one long
	// stream. A restart in that window brought the new process up on the relay
	// port and then failed on the admin one until the supervisor gave up,
	// leaving nothing running once the old process finished (2026-09-24).
	errs := make([]error, len(s.listeners))
	var wg sync.WaitGroup
	for i, l := range s.listeners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = l.srv.Shutdown(shutdownCtx)
		}()
	}
	wg.Wait()
	// Same deadline for all of them, and the relay's error wins: a page cut
	// short is not worth reporting over a severed stream.
	var err error
	for _, e := range errs {
		if e != nil {
			err = e
			break
		}
	}

	if errors.Is(err, context.DeadlineExceeded) {
		s.log.Warn("grace period expired with requests still in flight")
		return nil
	}
	if err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	s.log.Info("shutdown complete")
	return nil
}
