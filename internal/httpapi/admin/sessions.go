package admin

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/store"
)

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
		// The session cookie is SameSite=Strict, but "site" is the
		// registrable domain: a page on another port of this host, or a
		// sibling subdomain, still sends it. So a change must also come from
		// this origin — what the browser says in Sec-Fetch-Site or Origin.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if reason := httpx.CrossSite(r); reason != "" {
				httpx.WriteError(w, http.StatusForbidden, "permission_error",
					"this request came from another site ("+reason+")")
				return
			}
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
		httpx.WriteError(w, http.StatusTooManyRequests, httpx.KindRateLimit, "too many attempts")
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
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not start a session")
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
	httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest,
		"pass ?token=<sign-in link>, or POST your password to this path")
}

// handleAdminLogin exchanges the password for a session.
func (s *Admin) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Password) == "" {
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, "password is required")
		return
	}

	ip := httpx.ClientIP(r.Context())
	if !s.anonLimiter.AllowPerMinute("admin-login:"+ip, s.cfg.Limits.AnonPerMinute) {
		httpx.WriteError(w, http.StatusTooManyRequests, httpx.KindRateLimit, "too many attempts")
		return
	}

	ok, err := s.store.VerifyAdmin(r.Context(), body.Password)
	if errors.Is(err, store.ErrNoAdmin) {
		httpx.WriteError(w, http.StatusConflict, "needs_setup",
			"no account has been set up yet")
		return
	}
	if err != nil {
		s.log.Error("verify admin password", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not sign in")
		return
	}
	if !ok {
		// Same message and the same work either way: no oracle for whether the
		// password merely had the wrong characters.
		httpx.WriteError(w, http.StatusUnauthorized, "authentication_error", "incorrect password")
		return
	}
	s.startSession(w, r, "password")
}

func (s *Admin) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil {
		s.sessions.destroy(c.Value)
	}
	clearSessionCookie(w)
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "signed out"})
}

func (s *Admin) handleAdminMe(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"authenticated": true}
	if updated, err := s.store.AdminUpdatedAt(r.Context()); err == nil {
		out["password_updated_at"] = updated.UTC().Format(time.RFC3339)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// issueSession mints a session and sets the cookie, leaving the caller to
// decide what the response body is — the password endpoints answer with JSON,
// a sign-in link answers with a redirect.
func (s *Admin) issueSession(w http.ResponseWriter, r *http.Request) (time.Time, error) {
	token, expires, err := s.sessions.create()
	if err != nil {
		return time.Time{}, err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Expires:  expires,
		// Secure follows the connection rather than being fixed either way.
		//
		// Hard-coding it false was right for the common case and wrong for the
		// deployment most likely to be exposed: behind a TLS-terminating proxy
		// the browser speaks HTTPS the whole way, and the session cookie was
		// still marked as safe to send in clear. Hard-coding it true would be
		// worse — on a LAN or through an SSH tunnel the cookie would never be
		// sent at all and sign-in would fail with nothing to show for it.
		Secure: httpx.OverTLS(r, s.trustedProxies),
	})
	return expires, nil
}

// startSession issues the cookie and answers with when it lapses.
func (s *Admin) startSession(w http.ResponseWriter, r *http.Request, how string) {
	expires, err := s.issueSession(w, r)
	if err != nil {
		s.log.Error("create admin session", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not start a session")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"via":           how,
		"expires_at":    expires.UTC().Format(time.RFC3339),
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1,
	})
}
