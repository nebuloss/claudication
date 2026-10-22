package gateway

import (
	"errors"
	"net/http"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/service/install"
)

// handleInstall serves the script a client's install command runs:
//
//	/install?token=<t>&shell=sh|ps1
//
// The token is the only credential, and it is long, random, minutes old and
// good for one fetch. Each request still spends the unauthenticated budget,
// so guessing at tokens costs what guessing at keys does. The fetch that
// takes a link marks its client set up, which is when the client's page
// stops offering the key and the command.
//
// What goes wrong is said twice: in the status, which is what `curl -f`
// refuses on and `irm` throws on, and in the body, as a script that fails
// with the reason — never a page, since what comes back is being piped into a
// shell. 401 for a token that is missing, unknown or expired, 410 for one
// already used; 400 for a shell the address cannot mean.
func (s *Gateway) handleInstall(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	shell := q.Get("shell")
	ps := shell == "ps1"
	fail := func(status int, msg string) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(install.Failure(ps, msg)))
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// It carries a key: no cache anywhere may keep it.
	w.Header().Set("Cache-Control", "no-store")

	ip := httpx.ClientIP(r.Context())
	if !s.anonLimiter.AllowPerMinute(ip, s.cfg.Limits.AnonPerMinute) {
		fail(http.StatusTooManyRequests, "too many requests; try again in a minute.")
		return
	}
	if shell != "" && shell != "sh" && shell != "ps1" {
		fail(http.StatusBadRequest, "shell must be sh or ps1.")
		return
	}
	if shell == "" {
		shell = "sh"
	}
	bundle, err := s.install.Take(q.Get("token"), ip, shell)
	switch {
	case errors.Is(err, install.ErrUsed):
		fail(http.StatusGone, err.Error()+".")
		return
	case err != nil:
		fail(http.StatusUnauthorized, err.Error()+".")
		return
	}
	// An update's client is configured already, and its fetch is recorded as
	// the update link's own use (install.Bundle, Update).
	if !bundle.Update {
		if err := s.store.MarkConfigured(r.Context(), bundle.ClientID); err != nil {
			s.log.Warn("could not mark a client configured", "client", bundle.ClientID, "err", err)
		}
	}
	// The address the files point at is now the one this machine has: what
	// an update the client's agent asks for writes again (internal/httpapi/
	// agent), rather than an address read off its request.
	if bundle.Base != "" {
		if err := s.store.SetClientBase(r.Context(), bundle.ClientID, bundle.Base); err != nil {
			s.log.Warn("could not record a client's address", "client", bundle.ClientID, "err", err)
		}
	}
	script := install.Shell(bundle)
	if ps {
		script = install.PowerShell(bundle)
	}
	_, _ = w.Write([]byte(script))
}
