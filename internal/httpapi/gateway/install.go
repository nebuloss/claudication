package gateway

import (
	"net/http"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/service/install"
)

// handleInstall serves the script a key's install command runs:
//
//	/install?token=<t>&shell=sh|ps1&client=<id>[,<id>…]
//
// The token is the only credential, and it is long, random and minutes old.
// Each request still spends the unauthenticated budget, so guessing at tokens
// costs what guessing at keys does.
//
// What goes wrong is said twice: in the status, which is what `curl -f`
// refuses on and `irm` throws on, and in the body, as a script that fails
// with the reason — never a page, since what comes back is being piped into a
// shell. 401 for a token that is missing, unknown or expired; 400 for a shell
// or a client the address cannot mean.
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
	bundle, ok := s.install.Get(q.Get("token"))
	if !ok {
		fail(http.StatusUnauthorized,
			"this install command has expired or its token is wrong. Make a new one on the key's Clients page.")
		return
	}
	bundle = bundle.For(q.Get("client"))
	if len(bundle.Files) == 0 {
		fail(http.StatusBadRequest, "no client by that name: use claude-code, codex, opencode or crush.")
		return
	}
	script := install.Shell(bundle)
	if ps {
		script = install.PowerShell(bundle)
	}
	_, _ = w.Write([]byte(script))
}
