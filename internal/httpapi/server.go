// Package httpapi serves claudication's HTTP surface.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"claudication/internal/config"
	"claudication/internal/oauth"
	"claudication/internal/pool"
	"claudication/internal/secret"
	"claudication/internal/store"
	"claudication/internal/upstream"
	"claudication/internal/version"
)

type Server struct {
	cfg            config.Config
	log            *slog.Logger
	store          *store.Store
	keyLimiter     *limiter
	anonLimiter    *limiter
	budgets        *budgets
	trustedProxies []*net.IPNet
	httpServer     *http.Server
	adminServer    *http.Server
	adminAddr      string
	stopSweeper    chan struct{}
	sealer         *secret.Sealer
	pool           *pool.Pool
	relay          *upstream.Relay
	pending        *oauth.Pending
	sessions       *sessions
	httpClient     *http.Client
	startedAt      time.Time

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

func New(cfg config.Config, log *slog.Logger, st *store.Store, sealer *secret.Sealer) (*Server, error) {
	trusted, err := parseTrustedProxies(cfg.TrustedProxies)
	if err != nil {
		return nil, fmt.Errorf("trusted-proxies: %w", err)
	}

	s := &Server{
		cfg:            cfg,
		log:            log,
		store:          st,
		keyLimiter:     newLimiter(),
		anonLimiter:    newLimiter(),
		budgets:        newBudgets(),
		trustedProxies: trusted,
		stopSweeper:    make(chan struct{}),
		sealer:         sealer,
		pending:        oauth.NewPending(15 * time.Minute),
		sessions:       newSessions(),
		startedAt:      time.Now(),
		// Upstream calls made by the gateway itself: token exchange, refresh,
		// credential probes. Short timeout, because these are all small.
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}

	s.pool = pool.New(st, sealer, s.httpClient, log)
	s.relay = &upstream.Relay{
		Pool:        s.pool,
		Log:         log,
		Attribution: cfg.Passthrough.ClaudeCodeAttribution,
		// Relayed inference gets its own client with NO client-level timeout:
		// a streaming response legitimately runs for many minutes, and a
		// Timeout here would sever it mid-flight. The per-request context
		// carries the real deadline instead.
		Client: &http.Client{
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
		},
	}

	// With admin-listen set, this one drops the admin API and the UI; they
	// move to adminServer below. Unset, it keeps serving both.
	split := cfg.AdminListen != ""
	s.httpServer = &http.Server{
		Addr:    cfg.Listen,
		Handler: s.routes(role{gateway: true, admin: !split}),
		// No WriteTimeout: responses are long-lived SSE streams and a write
		// deadline would sever them mid-flight. ReadHeaderTimeout still
		// protects against slowloris on the request side.
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	if split {
		s.adminServer = &http.Server{
			Addr:              cfg.AdminListen,
			Handler:           s.routes(role{admin: true}),
			ReadHeaderTimeout: 30 * time.Second,
			IdleTimeout:       120 * time.Second,
			ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		}
	}
	return s, nil
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

	// The relay: Anthropic in, Anthropic out.
	if r0.gateway {
		mux.Handle("GET /v1/models", s.requireAPIKey(http.HandlerFunc(s.handleModels)))
		mux.Handle("POST /v1/messages", s.requireAPIKey(http.HandlerFunc(s.handleMessages)))
		mux.Handle("POST /v1/messages/count_tokens", s.requireAPIKey(http.HandlerFunc(s.handleCountTokens)))
	}

	if r0.admin {
		// Admin API. Setup and sign-in are outside requireAdmin: a gateway with no
		// password yet has nothing to authenticate against.
		//
		// These two change state without a cookie to protect them, so they carry
		// their own cross-site check — see sameSiteOnly. Everything under
		// requireAdmin is already covered by the session cookie being
		// SameSite=Strict.
		mux.HandleFunc("GET /admin/setup", s.handleSetupStatus)
		mux.Handle("POST /admin/setup", sameSiteOnly(http.HandlerFunc(s.handleSetup)))
		mux.Handle("POST /admin/session", sameSiteOnly(http.HandlerFunc(s.handleAdminLogin)))
		mux.HandleFunc("DELETE /admin/session", s.handleAdminLogout)
		mux.HandleFunc("GET /admin/session", s.handleTokenSession)

		admin := func(h http.HandlerFunc) http.Handler { return s.requireAdmin(h) }
		mux.Handle("GET /admin/me", admin(s.handleAdminMe))
		mux.Handle("POST /admin/password", admin(s.handleChangePassword))
		mux.Handle("POST /admin/account/delete", admin(s.handleDeleteAdminAccount))
		mux.Handle("GET /admin/accounts", admin(s.handleListAccounts))
		mux.Handle("POST /admin/accounts/order", admin(s.handleReorderAccounts))
		mux.Handle("POST /admin/accounts/oauth/start", admin(s.handleOAuthStart))
		mux.Handle("POST /admin/accounts/oauth/complete", admin(s.handleOAuthComplete))
		mux.Handle("POST /admin/accounts/{id}/test", admin(s.handleTestAccount))
		mux.Handle("POST /admin/accounts/{id}/refresh", admin(s.handleRefreshAccount))
		mux.Handle("POST /admin/accounts/{id}/usage", admin(s.handleRefreshUsage))
		mux.Handle("POST /admin/accounts/{id}/disabled", admin(s.handleSetAccountDisabled))
		mux.Handle("DELETE /admin/accounts/{id}", admin(s.handleDeleteAccount))

		// Client API keys — the credential a Claude Code points at the gateway.
		mux.Handle("GET /admin/keys", admin(s.handleListKeys))
		mux.Handle("POST /admin/keys", admin(s.handleCreateKey))
		mux.Handle("PATCH /admin/keys/{id}", admin(s.handleUpdateKey))
		mux.Handle("DELETE /admin/keys/{id}", admin(s.handleDeleteKey))

		mux.Handle("GET /admin/config", admin(s.handleConfig))
		mux.Handle("GET /admin/overview", admin(s.handleOverview))
		mux.Handle("GET /admin/usage", admin(s.handleUsage))
		mux.Handle("GET /admin/requests", admin(s.handleRecentRequests))

	}

	// Anything under an API prefix that did not match above is a client error,
	// and it has to say so in the client's own language. Without these, an
	// unknown /v1/... path would fall through to the UI's catch-all and answer
	// a web page, which parses as neither JSON nor an explanation.
	for _, prefix := range []string{"/v1/", "/admin/", "/api/"} {
		mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusNotFound, "not_found", "no such endpoint: "+r.URL.Path)
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
	ui := s.staticHandler()
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A listener that serves only the relay has no UI and no sign-in link
		// to spend: everything that is not an API path it knows is a 404, in
		// the client's own language.
		if !r0.admin || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			writeError(w, http.StatusNotFound, "not_found", "no such endpoint: "+r.URL.Path)
			return
		}
		// no-referrer so a ?token= link cannot leak to Anthropic when the
		// consent tab opens; nosniff because we serve JS from the same origin
		// as user-supplied account data.
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Only a page load spends the link. Every request under this handler
		// carries the query string it was reached with, so a prefetched
		// favicon, a stylesheet, or a service worker fetching `/?token=…`
		// would each burn a single-use sign-in link and leave the operator
		// looking at "already used" on the request they actually made.
		if isNavigation(r) && s.tokenLogin(w, r, "/") {
			return
		}
		ui.ServeHTTP(w, r)
	}))

	var h http.Handler = mux
	h = s.withBodyLimit(h)
	h = s.withRecovery(h)
	h = s.withAccessLog(h)
	h = s.withRequestContext(h)
	return h
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"version": version.Version,
	})
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
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	// Comfortably inside the client's 3-second budget.
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	body, err := s.fetchModels(ctx, r.URL.RawQuery)
	if err != nil {
		s.log.Warn("model discovery failed; answering 502 so the client keeps its cached list",
			"err", err, "request_id", requestIDFrom(r.Context()))
		writeError(w, http.StatusBadGateway, "api_error", "could not reach the upstream model list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (s *Server) fetchModels(ctx context.Context, rawQuery string) ([]byte, error) {
	lease, err := s.pool.Acquire(ctx, "anthropic", nil)
	if err != nil {
		return nil, err
	}

	base := s.relay.BaseURL
	if base == "" {
		base = upstream.AnthropicBaseURL
	}
	url := base + "/v1/models"
	if rawQuery != "" {
		url += "?" + rawQuery
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+lease.AccessToken)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")

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

// Run serves until ctx is cancelled, then drains within the configured grace
// period.
//
// Both SIGINT and SIGTERM reach this through ctx. auth2api handles only
// SIGINT, so every `docker stop` and `systemctl stop` kills it before its
// buffers flush and severs in-flight streams mid-frame.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.Listen, err)
	}

	s.mu.Lock()
	s.addr = ln.Addr().String()
	s.mu.Unlock()

	go s.keyLimiter.runSweeper(s.stopSweeper, time.Minute, 10*time.Minute)
	go s.anonLimiter.runSweeper(s.stopSweeper, time.Minute, 10*time.Minute)
	go s.budgets.runSweeper(s.stopSweeper, 10*time.Minute, time.Hour)
	go s.runUsagePruner()
	go s.runUsagePoller(ctx)

	// The admin listener binds before anything is served, so a port clash is a
	// startup failure rather than a gateway that comes up with no way in.
	var adminLn net.Listener
	if s.adminServer != nil {
		adminLn, err = net.Listen("tcp", s.cfg.AdminListen)
		if err != nil {
			_ = ln.Close()
			return fmt.Errorf("listen on %s: %w", s.cfg.AdminListen, err)
		}
		s.mu.Lock()
		s.adminAddr = adminLn.Addr().String()
		s.mu.Unlock()
	}

	// Buffered for both, so neither goroutine blocks on a send once the other
	// has already reported and the select has moved on.
	errCh := make(chan error, 2)
	go func() {
		s.log.Info("listening",
			"addr", ln.Addr().String(),
			"serving", servingWhat(s.adminServer == nil),
			"version", version.Version,
			"state_dir", s.cfg.StateDir)
		if err := s.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	if adminLn != nil {
		go func() {
			s.log.Info("admin listening", "addr", adminLn.Addr().String(),
				"serving", "admin API and UI")
			if err := s.adminServer.Serve(adminLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
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

	err = s.httpServer.Shutdown(shutdownCtx)
	if s.adminServer != nil {
		// Same deadline for both, and the relay's error wins: an admin page
		// cut short is not worth reporting over a severed stream.
		if aerr := s.adminServer.Shutdown(shutdownCtx); err == nil {
			err = aerr
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
