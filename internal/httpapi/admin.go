package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nebuloss/claudication/internal/oauth"
	"github.com/nebuloss/claudication/internal/store"
	"github.com/nebuloss/claudication/internal/upstream"
)

const (
	sessionCookie = "claudication_admin"
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

type accountJSON struct {
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

	// What the subscription has left. Absent until a response has been seen.
	Quota *quotaJSON `json:"quota,omitempty"`
	// Traffic this gateway put through the account, over the report window.
	Requests int64 `json:"requests"`
	Tokens   int64 `json:"tokens"`
	// Position in the priority list, and whether this is the one that would
	// serve the next request.
	Position int  `json:"position"`
	Serving  bool `json:"serving"`
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

func toAccountJSON(a store.Account) accountJSON {
	out := accountJSON{
		ID:        a.ID,
		Provider:  a.Provider,
		Email:     a.Email,
		ExpiresAt: a.ExpiresAt.UTC().Format(time.RFC3339),
		CreatedAt: a.CreatedAt.UTC().Format(time.RFC3339),
		LastError: a.LastError,
		Expired:   a.Expired(),
		Disabled:  a.Disabled(),
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
			var rows []upstream.UsageLimit
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "could not read the request body: "+err.Error())
		return false
	}
	return true
}

// ── middleware ───────────────────────────────────────────────────────────

// requireAdmin admits a live session cookie and nothing else.
//
// It used to accept an admin-scoped API key too. That second path existed
// before there was an account, and keeping it would mean two ways to become
// admin — one of which a password change could not revoke.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication_error", "sign in first")
			return
		}
		if _, ok := s.sessions.lookup(c.Value); !ok {
			writeError(w, http.StatusUnauthorized, "authentication_error", "session has expired")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tokenLogin handles a `?token=<link>` sign-in link.
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
func (s *Server) tokenLogin(w http.ResponseWriter, r *http.Request, redirectTo string) bool {
	raw := strings.TrimSpace(r.URL.Query().Get("token"))
	if raw == "" {
		return false
	}

	ip := clientIPFrom(r.Context())
	if !s.anonLimiter.allow("token-login:"+ip, s.cfg.Limits.AnonPerMinute) {
		writeError(w, http.StatusTooManyRequests, "rate_limit", "too many attempts")
		return true
	}

	if err := s.store.SpendLoginLink(r.Context(), raw); err != nil {
		s.log.Warn("sign-in link rejected", "ip", ip)
		writeError(w, http.StatusUnauthorized, "authentication_error",
			"that sign-in link is unknown, expired, or already used")
		return true
	}

	if _, err := s.issueSession(w); err != nil {
		s.log.Error("create admin session", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not start a session")
		return true
	}
	s.log.Info("sign-in link spent", "ip", ip)

	// 303 so the follow-up is a GET regardless of how we got here, and so the
	// token-bearing URL is replaced in history rather than added to it.
	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
	return true
}

// handleTokenSession is the curl-friendly entry point: GET /admin/session?token=…
func (s *Server) handleTokenSession(w http.ResponseWriter, r *http.Request) {
	if s.tokenLogin(w, r, "/") {
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_request",
		"pass ?token=<sign-in link>, or POST your password to this path")
}

// ── accounts ─────────────────────────────────────────────────────────────

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.store.ListAccounts(r.Context())
	if err != nil {
		s.log.Error("list accounts", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not list accounts")
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

	out := make([]accountJSON, 0, len(accounts))
	// The listing arrives in priority order, so the first one that could serve
	// is the one that would — the same walk the pool does.
	served := false
	for i, a := range accounts {
		j := toAccountJSON(a)
		j.Position = i
		if u, ok := usage[a.ID]; ok {
			j.Requests = u.Requests
			j.Tokens = u.InputTokens + u.OutputTokens + u.CacheTokens
		}
		if !served && !a.Disabled() && a.Quota.Allowed() {
			j.Serving = true
			served = true
		}
		out = append(out, j)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accounts":    out,
		"window_days": int(s.cfg.Usage.Window().Hours() / 24),
	})
}

// handleReorderAccounts takes the whole priority list rather than a swap: two
// operators reordering at once would otherwise interleave into an order
// neither asked for, and the UI already knows the list it wants.
func (s *Server) handleReorderAccounts(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "ids must not be empty")
		return
	}
	if err := s.store.SetAccountOrder(r.Context(), body.IDs); err != nil {
		s.log.Error("reorder accounts", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not save the order")
		return
	}
	s.log.Info("account priority changed", "ip", clientIPFrom(r.Context()))
	s.handleListAccounts(w, r)
}

// handleRefreshUsage re-polls one account on demand, so an operator watching
// the screen does not have to wait out the interval.
func (s *Server) handleRefreshUsage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.refreshAccountUsage(r.Context(), id); err != nil {
		s.log.Warn("usage refresh failed", "account", id, "err", err)
		writeError(w, http.StatusBadGateway, "upstream_error",
			"could not read the subscription usage: "+err.Error())
		return
	}
	account, err := s.store.Account(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "no such account")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": toAccountJSON(account)})
}

func (s *Server) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string `json:"provider"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Provider == "" {
		body.Provider = "anthropic"
	}
	if body.Provider != "anthropic" {
		writeError(w, http.StatusBadRequest, "unsupported_provider",
			"only the anthropic provider is implemented so far")
		return
	}

	redirectURI := oauth.RedirectManual

	pkce, err := oauth.NewPKCE()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	state, err := oauth.NewState()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	expires := s.pending.Start(body.Provider, state, pkce, redirectURI)

	writeJSON(w, http.StatusOK, map[string]any{
		"provider":     body.Provider,
		"state":        state,
		"auth_url":     oauth.AnthropicAuthURL(state, pkce, redirectURI),
		"redirect_uri": redirectURI,
		"expires_at":   expires.UTC().Format(time.RFC3339),
		"instructions": "Open the URL, approve access, then paste the authorization code Anthropic shows you back here.",
	})
}

func (s *Server) handleOAuthComplete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider    string `json:"provider"`
		State       string `json:"state"`
		RedirectURL string `json:"redirect_url"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Provider == "" {
		body.Provider = "anthropic"
	}

	code, returnedState, err := oauth.ParseCallback(body.RedirectURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_callback", err.Error())
		return
	}
	// Trust the state we issued; only reject when the callback carries a
	// different one, since some responses omit it entirely.
	if returnedState != "" && returnedState != body.State {
		writeError(w, http.StatusBadRequest, "state_mismatch",
			"that response belongs to a different login attempt")
		return
	}

	attempt, err := s.pending.Peek(body.Provider, body.State)
	if err != nil {
		writeError(w, http.StatusBadRequest, "unknown_state", err.Error())
		return
	}

	// Replay the redirect the authorize request carried; a mismatch is
	// rejected by the token endpoint.
	res, err := oauth.ExchangeAnthropicCode(r.Context(), s.httpClient,
		code, attempt.PKCE.Verifier, body.State, attempt.RedirectURI)
	if err != nil {
		// The attempt stays live, so a corrected paste can be retried without
		// walking through the consent screen again.
		s.log.Warn("token exchange failed", "err", err)
		writeError(w, http.StatusBadGateway, "exchange_failed", err.Error())
		return
	}
	// Redeemed: the verifier is spent from here on.
	s.pending.Consume(body.State)

	email := res.Email
	if email == "" {
		email = "unknown@" + body.Provider
	}

	acct, err := s.store.UpsertAccount(r.Context(), s.sealer, store.Account{
		Provider:         body.Provider,
		Email:            email,
		AccountUUID:      res.AccountUUID,
		ExpiresAt:        res.ExpiresAt,
		RefreshExpiresAt: res.RefreshTokenExpiresAt,
	}, store.Tokens{AccessToken: res.AccessToken, RefreshToken: res.RefreshToken})
	if err != nil {
		s.log.Error("store account", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not store the account")
		return
	}

	s.log.Info("account authorised", "provider", acct.Provider, "email", acct.Email)

	// Read the subscription usage before answering. The poller would get to it
	// within five minutes, but the operator is looking at the screen now, and a
	// brand-new account showing nothing is exactly the gap worth closing.
	if _, err := s.refreshAccountUsage(r.Context(), acct.ID); err != nil {
		s.log.Debug("could not read usage for the new account", "err", err)
	} else if fresh, err := s.store.Account(r.Context(), acct.ID); err == nil {
		acct = fresh
	}

	writeJSON(w, http.StatusOK, map[string]any{"account": toAccountJSON(acct)})
}

func (s *Server) handleTestAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	acct, err := s.store.Account(r.Context(), id)
	if errors.Is(err, store.ErrAccountNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "no such account")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	tokens, err := s.store.AccountTokens(r.Context(), s.sealer, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
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

	result := upstream.ProbeAnthropic(r.Context(), s.httpClient, tokens.AccessToken, body.Model)
	if result.OK {
		s.store.MarkAccountUsed(r.Context(), id)
	} else {
		s.store.MarkAccountError(r.Context(), id, result.Error)
	}
	writeJSON(w, http.StatusOK, result)
}

// handleRefreshAccount forces a token refresh now.
//
// Through the pool, which serialises refreshes per account: rotating refresh
// tokens mean two at once leave the loser holding a spent one, and a button
// the operator presses while traffic is flowing is exactly how that happens.
func (s *Server) handleRefreshAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.Account(r.Context(), id); errors.Is(err, store.ErrAccountNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "no such account")
		return
	}

	if err := s.pool.Refresh(r.Context(), id); err != nil {
		writeError(w, http.StatusBadGateway, "refresh_failed", err.Error())
		return
	}

	acct, err := s.store.Account(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": toAccountJSON(acct)})
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteAccount(r.Context(), id); errors.Is(err, store.ErrAccountNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "no such account")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
