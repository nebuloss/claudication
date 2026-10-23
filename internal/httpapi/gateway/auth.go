package gateway

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"claudication/internal/api"
	"claudication/internal/httpapi/httpx"
	"claudication/internal/service/clients"
	"claudication/internal/store"
)

// extractCredential reads the presented key.
//
// Every header style a client uses is accepted: the gateway contract says
// Claude Code sends the credential "in one or both headers depending on
// which credential variable they set", OpenAI clients use Authorization, and
// @google/genai — gemini-cli pointed at a gateway — sends x-goog-api-key, or
// the key query parameter the Gemini API also takes.
func extractCredential(r *http.Request) string {
	if v := r.Header.Get("Authorization"); v != "" {
		if len(v) > 7 && strings.EqualFold(v[:7], "bearer ") {
			return strings.TrimSpace(v[7:])
		}
	}
	if v := strings.TrimSpace(r.Header.Get("X-Api-Key")); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.Header.Get("X-Goog-Api-Key")); v != "" {
		return v
	}
	return strings.TrimSpace(r.URL.Query().Get("key"))
}

// rejection is the row for a request refused before it reached the upstream.
//
// One constructor because the fields are the interesting part: no model, no
// tokens, and an address — which is the only identity a stranger's refused
// request has. A refusal of a key that exists carries the key, because that
// one is a client to fix rather than a stranger; the caller adds whatever else
// it knows, such as the model. Rejected is true, so every aggregate steps over
// it and only the request log shows it.
//
// Deliberately not used for the anonymous rate limiter's own refusal. That
// check runs before the key lookup precisely to keep a flood off the database,
// and writing a row there would hand back the cost it exists to avoid. A flood
// shows up as rejections up to the per-IP cap and then as nothing, which is
// what the cap means; the access log still records every one.
func rejection(r *http.Request, key store.APIKey, status int, reason string) store.UsageEvent {
	return store.UsageEvent{
		At:       time.Now(),
		KeyID:    key.ID,
		KeyName:  key.Name,
		Path:     r.URL.Path,
		Status:   status,
		Client:   api.ClientName(r.UserAgent()),
		Surface:  clients.Surface(r.UserAgent()),
		IP:       httpx.ClientIP(r.Context()),
		Rejected: true,
		Error:    reason,
	}
}

// requireAPIKey authenticates, applies the per-key rate limit and the token
// budget, and attaches the credential to the request context.
//
// Every refusal is answered in p's own envelope, as the rest of the request
// path is: a client can only act on an error it can parse, and Codex reads
// error.code where Claude Code reads error.type. The kinds are Anthropic's,
// which every dialect maps — authentication_error, rate_limit_error, api_error.
func (s *Gateway) requireAPIKey(p api.ErrorWriter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := httpx.ClientIP(r.Context())

		// The anonymous budget is checked here but only *spent* below, on
		// requests that turn out to be anonymous. Spending it unconditionally
		// billed every authenticated request against anon-per-minute as
		// well — and since this middleware also fronts /v1/messages, that
		// silently capped real traffic at 60 requests a minute rather than the
		// 600 the per-key limit documents. Claude Code's subagent fan-out
		// crosses 60/min routinely.
		//
		// Peeking first still keeps an unauthenticated flood off the database:
		// each failure charges the bucket, so once an IP has spent its budget
		// the peek refuses it before the lookup.
		anon := func() bool { return s.anonLimiter.AllowPerMinute(ip, s.cfg.Limits.AnonPerMinute) }
		if !s.anonLimiter.Peek(ip, s.cfg.Limits.AnonPerMinute) {
			p.WriteError(w, http.StatusTooManyRequests, api.KindRateLimit, "too many requests")
			return
		}

		cred := extractCredential(r)
		if cred == "" {
			anon()
			s.recorder.Record(rejection(r, store.APIKey{}, http.StatusUnauthorized, "missing API key"), 0)
			p.WriteError(w, http.StatusUnauthorized, api.KindAuthentication, "missing API key")
			return
		}

		key, err := s.store.Authenticate(r.Context(), cred)
		if errors.Is(err, store.ErrKeyNotFound) {
			anon()
			s.recorder.Record(rejection(r, store.APIKey{}, http.StatusUnauthorized, "invalid API key"), 0)
			p.WriteError(w, http.StatusUnauthorized, api.KindAuthentication, "invalid API key")
			return
		}
		if err != nil {
			s.log.Error("authenticate", "err", err, "request_id", httpx.RequestID(r.Context()))
			p.WriteError(w, http.StatusInternalServerError, api.KindAPI, "internal server error")
			return
		}
		// Used, whatever happens to the request next: a key that
		// authenticated is a client set up with it, by its install command or
		// by hand, and its page stops offering the key from now on. At most
		// one write a minute per key (TouchKey), the first of which also marks
		// the client configured.
		s.store.TouchKey(r.Context(), key.ID)

		// A client the operator switched off: refused before anything is
		// routed or counted against its limits, and recorded under its name,
		// because it is a client to switch back on rather than a stranger.
		// 403 permission_error, as for a model the key may not use: the
		// credential is real and is not allowed this, and a client stops
		// there rather than retrying.
		if key.Disabled {
			s.recorder.Record(rejection(r, key, http.StatusForbidden, "this client is disabled"), 0)
			p.WriteError(w, http.StatusForbidden, api.KindPermission,
				"this client is disabled; an administrator can switch it back on on its page in the admin UI")
			return
		}
		// A key's own allowance and the period it is measured over, falling back
		// to the gateway's per-minute default when the key sets none.
		count, period := key.RPMLimit, key.Period()
		if count == 0 {
			count, period = s.cfg.Limits.RequestsPerMinute, time.Minute
		}
		if !s.keyLimiter.Allow(key.ID, count, period) {
			// A key that exists and is going too fast: recorded under its own
			// name, because this one is a client to fix rather than a stranger.
			s.recorder.Record(rejection(r, key, http.StatusTooManyRequests,
				"rate limited by this gateway, not by the upstream"), 0)
			p.WriteError(w, http.StatusTooManyRequests, api.KindRateLimit, "too many requests")
			return
		}
		if !s.withinBudget(w, r, p, key) {
			return
		}

		ctx := httpx.WithAPIKey(r.Context(), key)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
