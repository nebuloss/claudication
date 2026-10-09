// Package admin is the admin API: everything the operator's UI calls, behind
// a session that only the admin password or a single-use sign-in link opens.
package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"claudication/internal/api"
	"claudication/internal/config"
	"claudication/internal/httpapi/httpx"
	"claudication/internal/oauth"
	"claudication/internal/pool"
	"claudication/internal/provider/anthropic"
	"claudication/internal/secret"
	accountsvc "claudication/internal/service/accounts"
	"claudication/internal/service/limits"
	"claudication/internal/service/settings"
	"claudication/internal/service/surfaces"
	"claudication/internal/service/titles"
	"claudication/internal/store"
)

// Admin serves the admin API: sign-in and the session it opens, the accounts
// and keys, the usage screens, and the switches an operator flips at runtime.
type Admin struct {
	cfg            *config.Config
	log            *slog.Logger
	store          *store.Store
	sealer         *secret.Sealer
	pool           *pool.Pool
	pending        *oauth.Pending
	poller         *accountsvc.Poller
	titles         *titles.Titler
	images         *settings.Switch
	docs           *settings.Switch
	surfaces       *surfaces.Surfaces
	protocols      api.Registry
	httpClient     *http.Client
	anonLimiter    *limits.Limiter
	budgets        *limits.Budgets
	trustedProxies []*net.IPNet
	startedAt      time.Time
	sessions       *sessions
	fetchModels    func(ctx context.Context) ([]byte, error)
	accountModels  func(ctx context.Context, accountID, rawQuery string) ([]byte, error)
}

// Deps is what an Admin is built from. Every field is required.
type Deps struct {
	Config         *config.Config
	Log            *slog.Logger
	Store          *store.Store
	Sealer         *secret.Sealer
	Pool           *pool.Pool
	Pending        *oauth.Pending
	Poller         *accountsvc.Poller
	Titles         *titles.Titler
	Images         *settings.Switch
	Docs           *settings.Switch
	Surfaces       *surfaces.Surfaces
	Protocols      api.Registry
	HTTPClient     *http.Client
	AnonLimiter    *limits.Limiter
	Budgets        *limits.Budgets
	TrustedProxies []*net.IPNet
	StartedAt      time.Time
	// Models is the upstream model list, as the client-facing API reads it.
	Models func(ctx context.Context) ([]byte, error)
	// AccountModels is the upstream model list as one account sees it.
	AccountModels func(ctx context.Context, accountID, rawQuery string) ([]byte, error)
}

// New builds an Admin with no session open.
func New(d Deps) *Admin {
	return &Admin{
		cfg: d.Config, log: d.Log, store: d.Store, sealer: d.Sealer, pool: d.Pool,
		pending: d.Pending, poller: d.Poller, titles: d.Titles, images: d.Images,
		docs: d.Docs, surfaces: d.Surfaces, protocols: d.Protocols,
		httpClient: d.HTTPClient, anonLimiter: d.AnonLimiter, budgets: d.Budgets,
		trustedProxies: d.TrustedProxies, startedAt: d.StartedAt,
		sessions: newSessions(), fetchModels: d.Models, accountModels: d.AccountModels,
	}
}

// Routes registers the admin API on mux.
func (s *Admin) Routes(mux *http.ServeMux) {
	// Admin API. Setup and sign-in are outside RequireAdmin: a gateway with no
	// password yet has nothing to authenticate against.
	//
	// These two change state without a cookie to protect them, so they carry
	// their own cross-site check — see httpx.SameSiteOnly. Everything under
	// RequireAdmin is already covered by the session cookie being
	// SameSite=Strict.
	mux.HandleFunc("GET /admin/setup", s.handleSetupStatus)
	mux.Handle("POST /admin/setup", httpx.SameSiteOnly(http.HandlerFunc(s.handleSetup)))
	mux.Handle("POST /admin/session", httpx.SameSiteOnly(http.HandlerFunc(s.handleAdminLogin)))
	mux.HandleFunc("DELETE /admin/session", s.handleAdminLogout)
	mux.HandleFunc("GET /admin/session", s.handleTokenSession)

	admin := func(h http.HandlerFunc) http.Handler { return s.RequireAdmin(h) }
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
	mux.Handle("GET /admin/accounts/{id}/models", admin(s.handleAccountModels))
	mux.Handle("POST /admin/accounts/{id}/models", admin(s.handleSetAccountModel))
	mux.Handle("DELETE /admin/accounts/{id}", admin(s.handleDeleteAccount))

	// Client API keys — the credential a Claude Code points at the gateway.
	mux.Handle("GET /admin/keys", admin(s.handleListKeys))
	mux.Handle("POST /admin/keys", admin(s.handleCreateKey))
	mux.Handle("PATCH /admin/keys/{id}", admin(s.handleUpdateKey))
	mux.Handle("DELETE /admin/keys/{id}", admin(s.handleDeleteKey))

	mux.Handle("GET /admin/config", admin(s.handleConfig))
	mux.Handle("GET /admin/models", admin(s.handleAdminModels))
	mux.Handle("POST /admin/surfaces/{id}", admin(s.handleSetSurface))
	mux.Handle("GET /admin/overview", admin(s.handleOverview))
	mux.Handle("GET /admin/usage", admin(s.handleUsage))
	mux.Handle("GET /admin/chats", admin(s.handleChats))
	mux.Handle("GET /admin/chats/{id}", admin(s.handleChat))
	mux.Handle("POST /admin/chat-titles", admin(s.handleSetChatTitles))
	mux.Handle("POST /admin/fit-images", admin(s.handleSetImageFit))
	mux.Handle("POST /admin/docs", admin(s.handleSetDocs))
	mux.Handle("GET /admin/requests", admin(s.handleRecentRequests))
	mux.Handle("GET /admin/requests/facets", admin(s.handleRequestFacets))
	mux.Handle("GET /admin/requests/export", admin(s.handleExportRequests))
}

const (
	SessionCookie = "claudication_admin"
	sessionTTL    = 12 * time.Hour
)

// sessions holds the live admin cookies.
//
// Kept in memory on purpose: a restart should log the operator out, and the
// admin cookie is never worth persisting next to the credentials it can read.
type sessions struct {
	mu   sync.Mutex
	live map[string]sessionEntry
}

type sessionEntry struct {
	expiresAt time.Time
}

func newSessions() *sessions { return &sessions{live: make(map[string]sessionEntry)} }

func (s *sessions) create() (string, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token := hex.EncodeToString(raw)
	expires := time.Now().Add(sessionTTL)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictLocked()
	s.live[token] = sessionEntry{expiresAt: expires}
	return token, expires, nil
}

func (s *sessions) lookup(token string) (sessionEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictLocked()
	e, ok := s.live[token]
	return e, ok
}

func (s *sessions) destroy(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.live, token)
}

// destroyAll ends every session. Changing or deleting the password has to
// invalidate the sessions opened under the old one, or it achieves nothing.
func (s *sessions) destroyAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.live)
}

func (s *sessions) evictLocked() {
	now := time.Now()
	for k, e := range s.live {
		if now.After(e.expiresAt) {
			delete(s.live, k)
		}
	}
}

// ── request/response shapes ──────────────────────────────────────────────

// AccountJSON is one connected account as the admin API reports it.
type AccountJSON struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Email         string `json:"email"`
	ExpiresAt     string `json:"expires_at"`
	CreatedAt     string `json:"created_at"`
	LastRefreshAt string `json:"last_refresh_at,omitempty"`
	LastUsedAt    string `json:"last_used_at,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	Expired       bool   `json:"expired"`
	Disabled      bool   `json:"disabled"`
	// When re-authorisation becomes unavoidable. Omitted when the provider did
	// not say; refreshing cannot push it back.
	RefreshExpiresAt string `json:"refresh_expires_at,omitempty"`
	ReauthDaysLeft   *int   `json:"reauth_days_left,omitempty"`
	NeedsReauthSoon  bool   `json:"needs_reauth_soon,omitempty"`
	// NeedsReauth is the terminal one: the refresh token was refused for good,
	// or its window has closed. Only a browser fixes it.
	NeedsReauth   bool   `json:"needs_reauth,omitempty"`
	RefreshDeadAt string `json:"refresh_dead_at,omitempty"`

	// What the subscription has left. Absent until a response has been seen.
	Quota *quotaJSON `json:"quota,omitempty"`
	// Traffic this gateway put through the account, over the report window.
	Requests int64 `json:"requests"`
	Tokens   int64 `json:"tokens"`
	// Position in the priority list, and whether this is the one that would
	// serve the next request.
	Position int  `json:"position"`
	Serving  bool `json:"serving"`
	// When a sidelined account comes back, if it is sitting one out. This is
	// the pool's in-memory state, so it is empty after a restart.
	CoolingUntil string `json:"cooling_until,omitempty"`
	// ModelsOff are the models turned off for this account, as switched.
	ModelsOff []string `json:"models_off,omitempty"`
}

// quotaJSON is what /api/oauth/usage reported: the two headline windows, plus
// the server's own normalised rows — the same list `/usage` prints, including
// the per-model weekly limits the headline windows do not cover.
//
// Percentages are 0-100, as the endpoint sends them.
type quotaJSON struct {
	UpdatedAt      string           `json:"updated_at"`
	FiveHourUtil   float64          `json:"five_hour_util"`
	FiveHourReset  string           `json:"five_hour_reset,omitempty"`
	FiveHourStatus string           `json:"five_hour_status,omitempty"`
	SevenDayUtil   float64          `json:"seven_day_util"`
	SevenDayReset  string           `json:"seven_day_reset,omitempty"`
	SevenDayStatus string           `json:"seven_day_status,omitempty"`
	Allowed        bool             `json:"allowed"`
	Limits         []usageLimitJSON `json:"limits,omitempty"`
}

// usageLimitJSON is one row of the usage display, titled the way the client
// titles it so the two read the same.
type usageLimitJSON struct {
	Title    string  `json:"title"`
	Kind     string  `json:"kind"`
	Percent  float64 `json:"percent"`
	Severity string  `json:"severity"`
	ResetsAt string  `json:"resets_at,omitempty"`
	IsActive bool    `json:"is_active"`
}

func toAccountJSON(a store.Account) AccountJSON {
	out := AccountJSON{
		ID:        a.ID,
		Provider:  a.Provider,
		Email:     a.Email,
		ExpiresAt: a.ExpiresAt.UTC().Format(time.RFC3339),
		CreatedAt: a.CreatedAt.UTC().Format(time.RFC3339),
		LastError: a.LastError,
		Expired:   a.Expired(),
		Disabled:  a.Disabled(),
		ModelsOff: a.ModelsOff,
	}
	if a.LastRefreshAt != nil {
		out.LastRefreshAt = a.LastRefreshAt.UTC().Format(time.RFC3339)
	}
	if a.LastUsedAt != nil {
		out.LastUsedAt = a.LastUsedAt.UTC().Format(time.RFC3339)
	}
	if left, ok := a.RefreshWindow(); ok {
		out.RefreshExpiresAt = a.RefreshExpiresAt.UTC().Format(time.RFC3339)
		// Ceiling, like the client's banner: "expires in 1 day" while any part
		// of that day remains.
		days := int((left + 24*time.Hour - time.Second) / (24 * time.Hour))
		if days < 0 {
			days = 0
		}
		out.ReauthDaysLeft = &days
		out.NeedsReauthSoon = a.NeedsReauthSoon()
	}
	// Outside that block on purpose: a refresh token refused for good is the
	// one account state a human has to act on, and it does not depend on the
	// expiry window being known — an account can be refused long before its
	// window runs out, and one whose window we never learned would otherwise
	// never say so.
	out.NeedsReauth = a.NeedsReauth()
	if a.RefreshDeadAt != nil {
		out.RefreshDeadAt = a.RefreshDeadAt.UTC().Format(time.RFC3339)
	}
	if a.Quota.Known() {
		q := quotaJSON{
			FiveHourUtil:   a.Quota.FiveHourUtil,
			FiveHourStatus: a.Quota.FiveHourStatus,
			SevenDayUtil:   a.Quota.SevenDayUtil,
			SevenDayStatus: a.Quota.SevenDayStatus,
			Allowed:        a.Quota.Allowed(),
		}
		if !a.Quota.UpdatedAt.IsZero() {
			q.UpdatedAt = a.Quota.UpdatedAt.UTC().Format(time.RFC3339)
		}
		if !a.Quota.FiveHourReset.IsZero() {
			q.FiveHourReset = a.Quota.FiveHourReset.UTC().Format(time.RFC3339)
		}
		if !a.Quota.SevenDayReset.IsZero() {
			q.SevenDayReset = a.Quota.SevenDayReset.UTC().Format(time.RFC3339)
		}
		if a.Quota.Detail != "" {
			var rows []anthropic.UsageLimit
			if err := json.Unmarshal([]byte(a.Quota.Detail), &rows); err == nil {
				for _, l := range rows {
					row := usageLimitJSON{
						Title:    l.Title(),
						Kind:     l.Kind,
						Percent:  l.Percent,
						Severity: l.Severity,
						IsActive: l.IsActive,
					}
					if l.ResetsAt != nil {
						row.ResetsAt = *l.ResetsAt
					}
					q.Limits = append(q.Limits, row)
				}
			}
		}
		out.Quota = &q
	}
	return out
}

// RequireAdmin admits a live session cookie and nothing else.
//
// It used to accept an admin-scoped API key too. That second path existed
// before there was an account, and keeping it would mean two ways to become
// admin — one of which a password change could not revoke.
func (s *Admin) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(SessionCookie)
		if err != nil {
			httpx.WriteError(w, http.StatusUnauthorized, "authentication_error", "sign in first")
			return
		}
		if _, ok := s.sessions.lookup(c.Value); !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "authentication_error", "session has expired")
			return
		}
		// Someone is looking, so the usage poller should work at its fast
		// rate. Recorded here rather than in each handler so every panel
		// counts, including ones that do not exist yet.
		s.poller.NoteActivity()
		next.ServeHTTP(w, r)
	})
}

// TokenLogin handles a `?token=<link>` sign-in link.
//
// The token is a short single-use handle, not an encoded credential: it is
// minted by `claudication login-url`, expires, and is spent the first time it
// is seen. So a link that ends up in browser history or a shell log is a spent
// link rather than a standing admin key, and losing one costs a link.
//
// What keeps the rest bounded: the exchange happens server-side, so nothing
// reaches JavaScript and the cookie stays HttpOnly; the response is an
// immediate 303 to the clean URL, so the token leaves the address bar before
// the page renders and replaces rather than extends history; the access log
// records r.URL.Path, which excludes the query; and the UI sends
// `Referrer-Policy: no-referrer`, so it cannot leak onward to Anthropic when
// the consent tab opens.
//
// Returns true when it has written a response.
func (s *Admin) TokenLogin(w http.ResponseWriter, r *http.Request, redirectTo string) bool {
	raw := strings.TrimSpace(r.URL.Query().Get("token"))
	if raw == "" {
		return false
	}

	ip := httpx.ClientIP(r.Context())
	if !s.anonLimiter.AllowPerMinute("token-login:"+ip, s.cfg.Limits.AnonPerMinute) {
		httpx.WriteError(w, http.StatusTooManyRequests, "rate_limit", "too many attempts")
		return true
	}

	if err := s.store.SpendLoginLink(r.Context(), raw); err != nil {
		s.log.Warn("sign-in link rejected", "ip", ip)
		httpx.WriteError(w, http.StatusUnauthorized, "authentication_error",
			"that sign-in link is unknown, expired, or already used")
		return true
	}

	if _, err := s.issueSession(w, r); err != nil {
		s.log.Error("create admin session", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not start a session")
		return true
	}
	s.log.Info("sign-in link spent", "ip", ip)

	// 303 so the follow-up is a GET regardless of how we got here, and so the
	// token-bearing URL is replaced in history rather than added to it.
	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
	return true
}

// handleTokenSession is the curl-friendly entry point: GET /admin/session?token=…
func (s *Admin) handleTokenSession(w http.ResponseWriter, r *http.Request) {
	if s.TokenLogin(w, r, "/") {
		return
	}
	httpx.WriteError(w, http.StatusBadRequest, "invalid_request",
		"pass ?token=<sign-in link>, or POST your password to this path")
}

// ── accounts ─────────────────────────────────────────────────────────────

func (s *Admin) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.store.ListAccounts(r.Context())
	if err != nil {
		s.log.Error("list accounts", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not list accounts")
		return
	}

	// How much this gateway put through each account, which is a different
	// question from how much of the subscription is spent: other clients share
	// the same Claude account.
	usage, err := s.store.AccountUsage(r.Context(), time.Now().Add(-s.cfg.Usage.Window()))
	if err != nil {
		s.log.Warn("account usage unavailable", "err", err)
		usage = map[string]store.UsageBucket{}
	}

	// Ask the pool which account would serve rather than working it out again
	// here. This walk used to approximate pick() and could not see a cooldown
	// at all — cooldowns are in memory, not in the row — so an account sitting
	// out a rate limit still wore the "serving" badge.
	status, err := s.pool.Status(r.Context(), "anthropic")
	if err != nil {
		s.log.Error("account pool status", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not read accounts")
		return
	}

	out := make([]AccountJSON, 0, len(accounts))
	for i, a := range accounts {
		j := toAccountJSON(a)
		j.Position = i
		if u, ok := usage[a.ID]; ok {
			j.Requests = u.Requests
			j.Tokens = u.InputTokens + u.OutputTokens + u.CacheTokens
		}
		j.Serving = a.ID == status.Serving
		if until, ok := status.Cooling[a.ID]; ok {
			j.CoolingUntil = until.UTC().Format(time.RFC3339)
		}
		out = append(out, j)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"accounts":    out,
		"window_days": int(s.cfg.Usage.Window().Hours() / 24),
		// What the server will do next, so the UI can re-ask at the rate the
		// figures actually change instead of guessing at one. Asking faster
		// than this only ever returns the same numbers again.
		"usage_poll_s": int(s.cfg.Usage.PollWatched.D().Seconds()),
	})
}

// handleReorderAccounts takes the whole priority list rather than a swap: two
// operators reordering at once would otherwise interleave into an order
// neither asked for, and the UI already knows the list it wants.
func (s *Admin) handleReorderAccounts(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if len(body.IDs) == 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "ids must not be empty")
		return
	}
	if err := s.store.SetAccountOrder(r.Context(), body.IDs); err != nil {
		s.log.Error("reorder accounts", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not save the order")
		return
	}
	s.log.Info("account priority changed", "ip", httpx.ClientIP(r.Context()))
	s.handleListAccounts(w, r)
}

// handleRefreshUsage re-polls one account on demand, so an operator watching
// the screen does not have to wait out the interval.
func (s *Admin) handleRefreshUsage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.poller.Refresh(r.Context(), id); err != nil {
		s.log.Warn("usage refresh failed", "account", id, "err", err)
		httpx.WriteError(w, http.StatusBadGateway, "upstream_error",
			"could not read the subscription usage: "+err.Error())
		return
	}
	account, err := s.store.Account(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "no such account")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"account": toAccountJSON(account)})
}

// handleOAuthStart begins a consent flow: for a new account, or, with
// account_id, to reconnect one already stored.
//
// A reconnect names the account to the provider (a login hint, as `claude auth
// login --email` sends) and is remembered against it, so completing it can
// check that the operator approved as that account and renew it in place.
func (s *Admin) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider  string `json:"provider"`
		AccountID string `json:"account_id"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if body.Provider == "" {
		body.Provider = "anthropic"
	}
	if body.Provider != "anthropic" {
		httpx.WriteError(w, http.StatusBadRequest, "unsupported_provider",
			"only the anthropic provider is implemented so far")
		return
	}

	var target store.Account
	if body.AccountID != "" {
		acct, err := s.store.Account(r.Context(), body.AccountID)
		if errors.Is(err, store.ErrAccountNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "no such account")
			return
		}
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		if acct.Provider != body.Provider {
			httpx.WriteError(w, http.StatusBadRequest, "unsupported_provider",
				"that account belongs to another provider")
			return
		}
		target = acct
	}

	redirectURI := anthropic.RedirectManual

	pkce, err := oauth.NewPKCE()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	state, err := oauth.NewState()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	expires := s.pending.StartFor(body.Provider, state, pkce, redirectURI, target.ID)

	out := map[string]any{
		"provider":     body.Provider,
		"state":        state,
		"auth_url":     anthropic.AuthURL(state, pkce, redirectURI, loginHint(target)),
		"redirect_uri": redirectURI,
		"expires_at":   expires.UTC().Format(time.RFC3339),
		"instructions": "Open the URL, approve access, then paste the authorization code Anthropic shows you back here.",
	}
	if target.ID != "" {
		out["account_id"] = target.ID
		out["email"] = target.Email
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// loginHint is the address to pre-fill on the provider's sign-in page, or
// nothing when there is no account or its address was never learned.
func loginHint(a store.Account) string {
	if a.ID == "" || strings.HasPrefix(a.Email, "unknown@") {
		return ""
	}
	return a.Email
}

// sameAccount reports whether a consent flow's tokens belong to a stored
// account: by the provider's account id where both sides have one, which
// survives a change of address, and by address otherwise.
func sameAccount(a store.Account, uuid, email string) bool {
	if a.AccountUUID != "" && uuid != "" {
		return a.AccountUUID == uuid
	}
	return email != "" && strings.EqualFold(a.Email, email)
}

// handleOAuthComplete redeems the code and stores what it bought.
//
// Three outcomes. A reconnect whose tokens belong to its account renews that
// account in place. An add whose address is already stored is the same
// renewal, reached the long way round — it used to be the only way, and it
// still must not create a second row. Anything else is a new account, last in
// the priority list.
//
// A reconnect that comes back as someone else is refused, and the tokens are
// handed back to the provider rather than kept. The browser decides who
// approves, not the gateway: one already signed in to another Claude account
// approves as that one whatever the login hint said, and storing the result
// would either add an account nobody asked for or, worse, put one person's
// subscription behind another's name.
func (s *Admin) handleOAuthComplete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider    string `json:"provider"`
		State       string `json:"state"`
		RedirectURL string `json:"redirect_url"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if body.Provider == "" {
		body.Provider = "anthropic"
	}

	code, returnedState, err := oauth.ParseCallback(body.RedirectURL)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_callback", err.Error())
		return
	}
	// Trust the state we issued; only reject when the callback carries a
	// different one, since some responses omit it entirely.
	if returnedState != "" && returnedState != body.State {
		httpx.WriteError(w, http.StatusBadRequest, "state_mismatch",
			"that response belongs to a different login attempt")
		return
	}

	attempt, err := s.pending.Peek(body.Provider, body.State)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "unknown_state", err.Error())
		return
	}

	// Replay the redirect the authorize request carried; a mismatch is
	// rejected by the token endpoint.
	res, err := anthropic.ExchangeCode(r.Context(), s.httpClient,
		code, attempt.PKCE.Verifier, body.State, attempt.RedirectURI)
	if err != nil {
		// The attempt stays live, so a corrected paste can be retried without
		// walking through the consent screen again.
		s.log.Warn("token exchange failed", "err", err)
		httpx.WriteError(w, http.StatusBadGateway, "exchange_failed", err.Error())
		return
	}
	// Redeemed: the verifier is spent from here on.
	s.pending.Consume(body.State)

	email := res.Email
	if email == "" {
		email = "unknown@" + body.Provider
	}
	tokens := store.Tokens{AccessToken: res.AccessToken, RefreshToken: res.RefreshToken}
	fresh := store.Account{
		Provider:         body.Provider,
		Email:            email,
		AccountUUID:      res.AccountUUID,
		ExpiresAt:        res.ExpiresAt,
		RefreshExpiresAt: res.RefreshTokenExpiresAt,
	}

	// Which stored account, if any, these tokens renew.
	var target store.Account
	if attempt.Account != "" {
		target, err = s.store.Account(r.Context(), attempt.Account)
		if errors.Is(err, store.ErrAccountNotFound) {
			s.handBack(res.RefreshToken, "the account was removed during its reconnect")
			httpx.WriteError(w, http.StatusNotFound, "not_found",
				"that account was removed while you were signing in; add it again instead")
			return
		}
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		if !sameAccount(target, res.AccountUUID, res.Email) {
			s.handBack(res.RefreshToken, "a reconnect came back as a different account")
			s.log.Warn("reconnect approved as a different account; nothing stored",
				"account", target.ID, "expected", target.Email, "approved_as", email)
			httpx.WriteError(w, http.StatusConflict, "wrong_account",
				"access was approved as "+email+", not "+target.Email+
					", so nothing was changed. The browser approves as whichever Claude account "+
					"it is signed in to: sign out of claude.ai there, or open the link in a "+
					"private window, sign in as "+target.Email+", and start the reconnect again.")
			return
		}
	} else if existing, err := s.store.AccountByEmail(r.Context(), body.Provider, email); err == nil {
		target = existing
	}

	var acct store.Account
	renewed := target.ID != ""
	if renewed {
		acct, err = s.store.RenewAccount(r.Context(), s.sealer, target.ID, fresh, tokens)
	} else {
		acct, err = s.store.UpsertAccount(r.Context(), s.sealer, fresh, tokens)
	}
	if err != nil {
		s.log.Error("store account", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not store the account")
		return
	}

	if renewed {
		// The backoff belonged to the credentials just replaced.
		s.pool.Reset(acct.ID)
		s.log.Info("account reconnected", "provider", acct.Provider, "email", acct.Email,
			"account", acct.ID)
	} else {
		s.log.Info("account authorised", "provider", acct.Provider, "email", acct.Email)
	}

	// Read the subscription usage before answering. The poller would get to it
	// within five minutes, but the operator is looking at the screen now, and a
	// brand-new account showing nothing is exactly the gap worth closing.
	if err := s.poller.Refresh(r.Context(), acct.ID); err != nil {
		s.log.Debug("could not read usage for the new account", "err", err)
	} else if fresh, err := s.store.Account(r.Context(), acct.ID); err == nil {
		acct = fresh
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"account": toAccountJSON(acct), "renewed": renewed})
}

// handBack revokes a refresh token the gateway was issued but will not keep.
//
// Best effort, as on delete: the provider being unreachable must not turn a
// refusal into an error, and an unrevoked grant ages out on its own. But not
// revoking at all would leave a live credential for an account nobody asked
// to connect, held by nothing.
func (s *Admin) handBack(refreshToken, why string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := anthropic.Revoke(ctx, s.httpClient, refreshToken, anthropic.ClientID); err != nil {
		s.log.Warn("could not revoke an unwanted grant; it will lapse on its own", "why", why, "err", err)
	}
}

func (s *Admin) handleTestAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	acct, err := s.store.Account(r.Context(), id)
	if errors.Is(err, store.ErrAccountNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "no such account")
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	tokens, err := s.store.AccountTokens(r.Context(), s.sealer, id)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	// Refresh first if the access token has expired, so "test" answers the
	// question the operator is actually asking: can this account serve traffic?
	//
	// Through the pool rather than directly, because the pool holds the
	// single-flight lock. Refresh tokens rotate, so a refresh here racing one
	// on the request path would leave the loser holding a spent token — which
	// is the failure this button exists to diagnose, not to cause.
	if acct.Expired() {
		if rerr := s.pool.Refresh(r.Context(), id); rerr == nil {
			if fresh, ferr := s.store.AccountTokens(r.Context(), s.sealer, id); ferr == nil {
				tokens = fresh
			}
		}
	}

	var body struct {
		Model string `json:"model"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)

	result := anthropic.Probe(r.Context(), s.httpClient, tokens.AccessToken, body.Model)
	if result.OK {
		s.store.MarkAccountUsed(r.Context(), id)
	} else {
		s.store.MarkAccountError(r.Context(), id, result.Error)
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

// handleRefreshAccount forces a token refresh now.
//
// Through the pool, which serialises refreshes per account: rotating refresh
// tokens mean two at once leave the loser holding a spent one, and a button
// the operator presses while traffic is flowing is exactly how that happens.
func (s *Admin) handleRefreshAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.Account(r.Context(), id); errors.Is(err, store.ErrAccountNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "no such account")
		return
	}

	if err := s.pool.Refresh(r.Context(), id); err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "refresh_failed", err.Error())
		return
	}

	acct, err := s.store.Account(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"account": toAccountJSON(acct)})
}

// handleDeleteAccount hands the credential back before forgetting it.
//
// Revoking first is the order the client's own /logout uses, and it is the
// only order that can work: once the row is gone the refresh token is gone
// with it, and there is nothing left to revoke. Forgetting a credential is not
// the same as releasing it — an account removed here used to stay live
// upstream until it aged out on its own.
// handleSetAccountDisabled pauses an account, or resumes it.
//
// Pausing keeps the credentials: the alternative was deleting the account,
// which revokes its refresh token upstream and means going through the browser
// consent flow again to undo. "Not this one this week" should not cost that.
func (s *Admin) handleSetAccountDisabled(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Disabled bool `json:"disabled"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	switch err := s.store.SetAccountDisabled(r.Context(), id, body.Disabled); {
	case errors.Is(err, store.ErrAccountNotFound):
		httpx.WriteError(w, http.StatusNotFound, "not_found", "no such account")
		return
	case err != nil:
		s.log.Error("set account disabled", "err", err, "account", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not update the account")
		return
	}
	s.log.Info("account availability changed", "account", id, "disabled", body.Disabled,
		"ip", httpx.ClientIP(r.Context()))

	acct, err := s.store.Account(r.Context(), id)
	if err != nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]bool{"disabled": body.Disabled})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAccountJSON(acct))
}

func (s *Admin) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Best-effort, and deliberately not fatal. The operator asked for this
	// account to be gone; an upstream that is down, slow or has already
	// forgotten the token must not be able to prevent that.
	if tokens, err := s.store.AccountTokens(r.Context(), s.sealer, id); err == nil {
		if err := anthropic.Revoke(r.Context(), s.httpClient,
			tokens.RefreshToken, anthropic.ClientID); err != nil {
			s.log.Warn("could not revoke the refresh token upstream; deleting locally anyway",
				"account", id, "err", err)
		} else {
			s.log.Info("refresh token revoked upstream", "account", id)
		}
	}

	if err := s.store.DeleteAccount(r.Context(), id); errors.Is(err, store.ErrAccountNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "no such account")
		return
	} else if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	s.log.Info("account removed", "account", id, "ip", httpx.ClientIP(r.Context()))
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleConfig reports every configuration value and where it came from.
//
// Read-only, and it stays that way. The config file is never written back —
// that is the rule this package's config loader exists to enforce, because an
// agent that re-serialises a YAML file destroys every comment the operator
// wrote in it. What this screen can do instead is answer the question that
// actually costs time: not "what is this set to" but "why is it that, and
// which of the three places do I change".
func (s *Admin) handleConfig(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"path":     s.cfg.Path,
		"settings": s.cfg.Settings(),
		// Reported alongside the file-and-environment settings rather than on
		// a screen of their own: they are configuration an operator reasons
		// about together with the rest, and separating them is how a switch
		// gets flipped and then lost.
		"surfaces": s.surfaces.State(),
		// Beside the surfaces for the same reason, and because it is the one
		// switch that decides whether the gateway spends the operator's
		// subscription on its own behalf. That belongs where they are already
		// looking at what this gateway is allowed to do.
		"chat_titles":         s.titles.On(),
		"chat_titles_capture": s.titles.Capturing(),
		"fit_images":          s.images.On(),
		// Empty unless a public docs page is both configured and published,
		// in which case the header links out to it.
		// Where the page is: its own address when it has one, otherwise the
		// relay's, which is where the relay serves it.
		"docs_url":     s.cfg.DocsPageURL(),
		"docs_enabled": s.docs.On(),
	})
}

// handleAdminModels lists the models the connected accounts can actually serve.
//
// The same upstream list /v1/models proxies, but reachable with an admin
// session instead of an API key, because the Setup screen needs it and the
// browser has a cookie rather than a key. Reusing fetchModels keeps one answer
// to "which models exist" rather than a second, drifting copy of it in the UI.
func (s *Admin) handleAdminModels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	body, err := s.fetchModels(ctx)
	if err != nil {
		s.log.Warn("could not read the upstream model list", "err", err)
		httpx.WriteError(w, http.StatusBadGateway, "api_error",
			"could not reach the upstream model list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// handleSetSurface turns one client-facing API on or off.
//
// It takes effect on the next request — there is no restart and nothing to
// drain — and it is allowed to leave every surface off. That state stops the
// gateway serving anything to anyone, which is the point of having the switch
// at all, so it is not second-guessed here.
func (s *Admin) handleSetSurface(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, ok := s.protocols.Find(id)
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "no such API surface")
		return
	}

	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "expected {\"enabled\": true|false}")
		return
	}
	if body.Enabled == nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "enabled is required")
		return
	}

	if err := s.surfaces.Set(r.Context(), id, *body.Enabled); err != nil {
		s.log.Error("could not store the API surface switch", "surface", id, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "api_error", "could not store the setting")
		return
	}
	s.log.Warn("API surface switched", "surface", id, "title", p.Title(), "enabled", *body.Enabled)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"surfaces": s.surfaces.State()})
}
