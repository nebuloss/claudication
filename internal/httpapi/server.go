// Package httpapi is where the gateway's HTTP surface is put together.
//
// It is the composition root and nothing else: it builds the services, hands
// each handler set what it needs, and decides which listener serves which.
// The handlers themselves live one level down, one package per surface:
//
//	httpx    the plumbing all of them share: errors, context, middleware
//	gateway  the client-facing APIs, behind an API key
//	admin    the admin API, behind a session
//	web      the embedded UI, the public docs page, the welcome page
//
// None of them imports another. What one needs from another — the model list,
// the sign-in link at the root — is handed across here, as a function.
package httpapi

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"claudication/internal/api"
	anthropicapi "claudication/internal/api/anthropic"
	"claudication/internal/api/openai"
	"claudication/internal/config"
	"claudication/internal/httpapi/admin"
	"claudication/internal/httpapi/gateway"
	"claudication/internal/httpapi/httpx"
	"claudication/internal/httpapi/web"
	"claudication/internal/memlimit"
	"claudication/internal/oauth"
	"claudication/internal/pool"
	"claudication/internal/provider"
	"claudication/internal/provider/anthropic"
	"claudication/internal/relay"
	"claudication/internal/relay/passes"
	accountsvc "claudication/internal/service/accounts"
	"claudication/internal/service/install"
	"claudication/internal/service/limits"
	modelsvc "claudication/internal/service/models"
	"claudication/internal/service/settings"
	"claudication/internal/service/surfaces"
	"claudication/internal/service/titles"
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
	cfg            *config.Config
	log            *slog.Logger
	store          *store.Store
	keyLimiter     *limits.Limiter
	anonLimiter    *limits.Limiter
	budgets        *limits.Budgets
	recorder       *usagesvc.Recorder
	trustedProxies []*net.IPNet
	adminAddr      string
	stopSweeper    chan struct{}
	pool           *pool.Pool
	relay          *relay.Relay
	httpClient     *http.Client
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
	surfaces  *surfaces.Surfaces
	images    *settings.Switch
	docs      *settings.Switch

	// models is the model list and its aliases, which all three surfaces read.
	models *modelsvc.Catalog

	// The handler sets, one per surface. See the package comment.
	gateway *gateway.Gateway
	admin   *admin.Admin
	web     *web.Site

	// listeners are the addresses served, the relay's first; adminListener is
	// the admin's own, when admin-listen gives it one.
	listeners     []*listener
	adminListener *listener

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
		anonLimiter:    limits.NewLimiter(),
		budgets:        limits.NewBudgets(st),
		trustedProxies: trusted,
		stopSweeper:    make(chan struct{}),
		trimmer:        memlimit.NewTrimmer(log),
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
		openai.New(store.RoleLarge),
	}
	s.surfaces = surfaces.New(s.protocols, s.store)
	// The fallback is every surface serving, which is the state the gateway
	// was in before the switches existed.
	if err := s.surfaces.Load(ctx); err != nil {
		s.log.Warn("could not read the API surface switches; serving every surface", "err", err)
	}

	// And again: off is the pass-through rule, so a switch we cannot read
	// leaves the caller's bytes alone.
	s.images = settings.NewSwitch(s.store, passes.FitImagesSetting, false)
	if err := settings.Load(ctx, s.store, s.images); err != nil {
		s.log.Warn("could not read the image-fit switch; leaving it off", "err", err)
	}

	// Opposite default again: configuring docs-listen is the decision to serve
	// the page, so a switch we cannot read leaves it serving.
	s.docs = settings.NewSwitch(s.store, web.DocsSetting, false)
	if err := settings.Load(ctx, s.store, s.docs); err != nil {
		s.log.Warn("could not read the docs switch; serving the page anyway", "err", err)
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
	s.providers = provider.Registry{anthropic.Accounts{}}

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
	s.relay = &relay.Relay{
		Wire: anthropic.Provider{},
		Pool: s.pool,
		Log:  log,
		Passes: passes.Default(passes.Options{
			Attribution: cfg.Passthrough.ClaudeCodeAttribution,
			FitImages:   s.images.On,
			Images:      passes.NewImageCache(passes.DefaultImageCacheBytes),
			Log:         log,
		}),
		StallTimeout: cfg.Passthrough.StallTimeout.D(),
		Client:       newRelayClient(),
	}

	// Chat titles, once the relay and the recorder they use exist. A failure
	// to read the switches leaves titling off, because it is the one thing
	// that spends the operator's subscription on the gateway's own behalf and
	// doing that by accident is not a state worth reaching.
	titler := titles.New(titles.Deps{Store: st, Relay: s.relay, Record: s.recorder.Record, Log: log})
	if err := titler.Load(ctx); err != nil {
		log.Warn("could not read the chat-title switch; leaving it off", "err", err)
	}
	return titler
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
		Config: cfg, Log: log, Store: st, Pool: s.pool, Relay: s.relay, HTTPClient: s.httpClient,
	})
	s.gateway = gateway.New(gateway.Deps{
		Config: cfg, Log: log, Store: st,
		KeyLimiter: s.keyLimiter, AnonLimiter: s.anonLimiter, Budgets: s.budgets,
		Recorder: s.recorder, Pool: s.pool, Relay: s.relay, Titles: titler,
		Trimmer: s.trimmer, Surfaces: s.surfaces, Protocols: s.protocols,
		Install: installs, Models: s.models,
	})
	s.admin = admin.New(admin.Deps{
		Config: cfg, Log: log, Store: st, Pool: s.pool,
		Pending: oauth.NewPending(15 * time.Minute), Poller: s.poller,
		Titles: titler, Images: s.images, Docs: s.docs, Surfaces: s.surfaces,
		Protocols: s.protocols, HTTPClient: s.httpClient, Providers: s.providers,
		AnonLimiter: s.anonLimiter, Budgets: s.budgets,
		TrustedProxies: s.trustedProxies, StartedAt: time.Now(),
		Models: s.models, Install: installs,
	})
	s.web = web.New(web.Deps{
		Config: cfg, Log: log, Store: st, Docs: s.docs, Surfaces: s.surfaces,
		Models: s.models, Files: webdist,
	})
}

// listener is one address the gateway serves, what it serves there, and the
// line it logs when it starts.
type listener struct {
	addr  string
	srv   *http.Server
	msg   string
	attrs []any
	ln    net.Listener
}

func (s *Server) newListener(addr string, h http.Handler, msg string, attrs ...any) *listener {
	return &listener{
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
}

// buildListeners decides which listener serves which surface. The relay's is
// always first: its address is Addr, and its error is the one reported.
func (s *Server) buildListeners() {
	cfg := s.cfg
	// With admin-listen set, the relay's listener drops the admin API and the
	// UI; they move to a listener of their own. Unset, it keeps serving both.
	split := cfg.AdminListen != ""
	// The relay also carries the public page, at the root it otherwise
	// answers 404 on. That is where someone pointing a client at this
	// gateway is already looking: they have the address, they typed it,
	// and until now it told them nothing. Behind the switch, so it is off
	// until an operator asks for it.
	//
	// Unless the admin UI is here too, in which case the root is the admin
	// UI and the page has nowhere to go: on a single-listener deployment
	// the whole surface is already private, and a public page would be a
	// contradiction rather than a feature.
	s.listeners = []*listener{s.newListener(cfg.Listen,
		s.routes(role{gateway: true, admin: !split, docs: split}),
		"listening", "serving", servingWhat(!split),
		"version", version.Version, "state_dir", cfg.StateDir)}
	if split {
		s.adminListener = s.newListener(cfg.AdminListen, s.routes(role{admin: true}),
			"admin listening", "serving", "admin API and UI")
		s.listeners = append(s.listeners, s.adminListener)
	}
	// And optionally on an address of its own, for a deployment that wants the
	// instructions somewhere the relay is not. Not needed to have the page —
	// the relay serves it above — but it is the way to publish it without
	// publishing /v1 alongside.
	if cfg.DocsListen != "" {
		s.listeners = append(s.listeners, s.newListener(cfg.DocsListen, s.routes(role{docs: true}),
			"docs listening", "serving", "the public docs page"))
	}
}

// role says which surfaces a listener serves.
//
// Both on one listener is the default and is right on a machine only the
// operator can reach. Split, the relay can be published while the admin API
// and UI stay on an address that is not — a separation the gateway enforces
// itself rather than trusting a proxy to.
type role struct {
	gateway bool
	admin   bool
	// docs serves the public docs page, and only that: one document at /, the
	// assets it needs, and one read-only endpoint behind it. No session, no
	// relay, nothing that can change anything.
	docs bool
}

// rootMode is what a listener's root serves.
type rootMode int

const (
	// rootNothing is a relay-only listener, where every path is an API path.
	rootNothing rootMode = iota
	// rootAdmin is the admin single-page app, and the sign-in link it spends.
	rootAdmin
	// rootDocs is the public docs page.
	rootDocs
	// rootWelcome is that same address with the docs page switched off: a
	// short page saying what the address is, and nothing about this gateway.
	rootWelcome
)

// rootMode decides which, per request, because the docs page sits behind a
// switch an operator can flip while the gateway runs.
//
// The order is the precedence. The admin UI owns the root wherever it is
// served: a listener carrying both roles is a single-listener deployment,
// where the whole surface is private already and a public page beside it would
// be a contradiction rather than a feature.
func (s *Server) rootMode(r0 role) rootMode {
	switch {
	case r0.admin:
		return rootAdmin
	case r0.docs && s.docs.On():
		return rootDocs
	case r0.docs:
		return rootWelcome
	default:
		return rootNothing
	}
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

	if r0.docs {
		mux.HandleFunc("GET /api/docs", s.web.HandleDocsInfo)
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
	// One handler per document, built once and chosen per request: which of
	// them the root serves depends on a switch an operator can flip while the
	// gateway is running.
	adminUI := s.web.StaticHandler("index.html")
	docsUI := s.web.StaticHandler("docs.html")
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		notFound := func() {
			httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such endpoint: "+r.URL.Path)
		}
		// Everything below serves documents, so nothing below answers a write.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			notFound()
			return
		}

		switch s.rootMode(r0) {
		case rootAdmin:
			setDocumentHeaders(w)
			// Only a page load spends the link. Every request under this
			// handler carries the query string it was reached with, so a
			// prefetched favicon, a stylesheet, or a service worker fetching
			// `/?token=…` would each burn a single-use sign-in link and leave
			// the operator looking at "already used" on the request they
			// actually made.
			if httpx.IsNavigation(r) && s.admin.TokenLogin(w, r, "/") {
				return
			}
			adminUI.ServeHTTP(w, r)

		case rootDocs:
			setDocumentHeaders(w)
			docsUI.ServeHTTP(w, r)

		case rootWelcome:
			// The root is the one path a person reaches by typing rather than
			// by calling, so it answers in HTML. Everything else is a client
			// that asked for an endpoint and gets the shape it can parse.
			if r.URL.Path == "/" {
				web.ServeWelcome(w, r)
				return
			}
			notFound()

		default:
			// A listener serving only the relay: every path it did not claim
			// above is an API path it does not have.
			notFound()
		}
	}))

	var h http.Handler = mux
	h = httpx.WithBodyLimit(h, s.cfg.Limits.MaxBodyBytes)
	h = httpx.WithRecovery(h, s.log)
	h = httpx.WithAccessLog(h, s.log)
	h = httpx.WithRequestContext(h, s.trustedProxies)
	return h
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"version": version.Version,
	})
}

// Run serves until ctx is cancelled, then drains within the configured grace
// period.
//
// Both SIGINT and SIGTERM reach this through ctx. auth2api handles only
// SIGINT, so every `docker stop` and `systemctl stop` kills it before its
// buffers flush and severs in-flight streams mid-frame.
func (s *Server) Run(ctx context.Context) error {
	// Every listener binds before anything is served, so a port clash on the
	// admin or docs address is a startup failure rather than a gateway that
	// comes up with no way in.
	for i, l := range s.listeners {
		ln, err := net.Listen("tcp", l.addr)
		if err != nil {
			for _, bound := range s.listeners[:i] {
				_ = bound.ln.Close()
			}
			return fmt.Errorf("listen on %s: %w", l.addr, err)
		}
		l.ln = ln
	}
	s.mu.Lock()
	s.addr = s.listeners[0].ln.Addr().String()
	if s.adminListener != nil {
		s.adminAddr = s.adminListener.ln.Addr().String()
	}
	s.mu.Unlock()

	// The background work starts only once every listener is bound. Started
	// before, a port clash on the admin or docs address returned with the
	// sweepers, the pruner and the poller still running against the database —
	// harmless in a process about to exit, and a test's state directory being
	// written to while it was removed (TestRunFailsOnAListenerClash, about one
	// run in five under load).
	go s.keyLimiter.RunSweeper(s.stopSweeper, time.Minute, 10*time.Minute)
	go s.anonLimiter.RunSweeper(s.stopSweeper, time.Minute, 10*time.Minute)
	go s.budgets.RunSweeper(s.stopSweeper, 10*time.Minute, time.Hour)
	go s.recorder.RunPruner(s.stopSweeper, 24*time.Hour)
	go s.poller.Run(ctx, s.stopSweeper, accountsvc.Tick)
	go s.trimmer.Run(s.stopSweeper, 5*time.Second)

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

	select {
	case err := <-errCh:
		close(s.stopSweeper)
		return err
	case <-ctx.Done():
	}

	grace := s.cfg.Shutdown.Grace.D()
	s.log.Info("shutting down", "grace", grace.String())

	shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()

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
	close(s.stopSweeper)

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
