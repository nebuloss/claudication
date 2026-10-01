package admin

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/store"
)

// The admin account is a single password, following singbox-admin: this is one
// operator's own gateway, not a multi-tenant service, so there is nobody to
// distinguish a username from.
//
// Sign-in is deliberately outside RequireAdmin, and so is setup — a gateway
// with no password yet has nothing to authenticate against, and the first
// person to reach it claims it. That is the same trade every self-hosted tool
// with a setup screen makes, and the reason CreateAdmin refuses to overwrite:
// the window closes the moment a password exists.

func (s *Admin) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	exists, err := s.store.AdminExists(r.Context())
	if err != nil {
		s.log.Error("check admin account", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not read the account")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"needs_setup":      !exists,
		"min_password_len": store.MinPasswordLength,
	})
}

func (s *Admin) handleSetup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}

	ip := httpx.ClientIP(r.Context())
	if !s.anonLimiter.AllowPerMinute("setup:"+ip, s.cfg.Limits.AnonPerMinute) {
		httpx.WriteError(w, http.StatusTooManyRequests, "rate_limit", "too many attempts")
		return
	}

	switch err := s.store.CreateAdmin(r.Context(), body.Password); {
	case errors.Is(err, store.ErrAdminExists):
		httpx.WriteError(w, http.StatusConflict, "already_setup",
			"an account already exists; sign in instead")
		return
	case errors.Is(err, store.ErrPasswordTooShort):
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	case err != nil:
		s.log.Error("create admin account", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not create the account")
		return
	}

	s.log.Info("admin account created", "ip", ip)
	s.startSession(w, r, "setup")
}

// handleChangePassword requires the current password even though the caller
// already holds a session: a browser someone walked away from should not be
// able to lock its owner out.
func (s *Admin) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}

	ip := httpx.ClientIP(r.Context())
	if !s.anonLimiter.AllowPerMinute("passwd:"+ip, s.cfg.Limits.AnonPerMinute) {
		httpx.WriteError(w, http.StatusTooManyRequests, "rate_limit", "too many attempts")
		return
	}

	switch err := s.store.ChangeAdminPassword(r.Context(), body.Current, body.New); {
	case errors.Is(err, store.ErrNoAdmin):
		httpx.WriteError(w, http.StatusNotFound, "not_found", "no account to change")
		return
	case errors.Is(err, store.ErrPasswordTooShort):
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	case err != nil:
		httpx.WriteError(w, http.StatusUnauthorized, "authentication_error", err.Error())
		return
	}

	// Every other session was opened with the old password. Ending them is the
	// point of changing it.
	s.sessions.destroyAll()
	s.log.Info("admin password changed", "ip", ip)
	s.startSession(w, r, "password change")
}

// handleDeleteAdminAccount returns the gateway to its first-run state.
func (s *Admin) handleDeleteAdminAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}

	// Deleting is destructive and irreversible from the UI, so it asks for the
	// password again rather than trusting the session alone.
	ok, err := s.store.VerifyAdmin(r.Context(), body.Password)
	if err != nil || !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "authentication_error", "password is incorrect")
		return
	}
	if err := s.store.DeleteAdmin(r.Context()); err != nil {
		s.log.Error("delete admin account", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not delete the account")
		return
	}

	s.sessions.destroyAll()
	clearSessionCookie(w)
	s.log.Warn("admin account deleted; gateway is back in first-run state",
		"ip", httpx.ClientIP(r.Context()))
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
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
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "password is required")
		return
	}

	ip := httpx.ClientIP(r.Context())
	if !s.anonLimiter.AllowPerMinute("admin-login:"+ip, s.cfg.Limits.AnonPerMinute) {
		httpx.WriteError(w, http.StatusTooManyRequests, "rate_limit", "too many attempts")
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
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not sign in")
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
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not start a session")
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
