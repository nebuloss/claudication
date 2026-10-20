package admin

import (
	"errors"
	"net/http"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/store"
	"claudication/internal/version"
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
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not read the account")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"needs_setup":      !exists,
		"min_password_len": store.MinPasswordLength,
		// The build, for the header the sign-in screen shares with the app:
		// "is this the version I just deployed" is asked of whichever screen
		// is open, and right after a deploy that is often this one. The same
		// string /health answers to anyone.
		"version": version.Version,
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
		httpx.WriteError(w, http.StatusTooManyRequests, httpx.KindRateLimit, "too many attempts")
		return
	}

	switch err := s.store.CreateAdmin(r.Context(), body.Password); {
	case errors.Is(err, store.ErrAdminExists):
		httpx.WriteError(w, http.StatusConflict, "already_setup",
			"an account already exists; sign in instead")
		return
	case errors.Is(err, store.ErrPasswordTooShort):
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, err.Error())
		return
	case err != nil:
		s.log.Error("create admin account", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not create the account")
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
		httpx.WriteError(w, http.StatusTooManyRequests, httpx.KindRateLimit, "too many attempts")
		return
	}

	switch err := s.store.ChangeAdminPassword(r.Context(), body.Current, body.New); {
	case errors.Is(err, store.ErrNoAdmin):
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no account to change")
		return
	case errors.Is(err, store.ErrPasswordTooShort):
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, err.Error())
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
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not delete the account")
		return
	}

	s.sessions.destroyAll()
	clearSessionCookie(w)
	s.log.Warn("admin account deleted; gateway is back in first-run state",
		"ip", httpx.ClientIP(r.Context()))
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
