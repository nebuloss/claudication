package gateway

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"claudication/internal/api"
	"claudication/internal/httpapi/httpx"
	"claudication/internal/store"
)

// extractCredential reads the presented key.
//
// Both header styles are accepted because the gateway contract says Claude
// Code sends the credential "in one or both headers depending on which
// credential variable they set", and OpenAI clients use Authorization.
func extractCredential(r *http.Request) string {
	if v := r.Header.Get("Authorization"); v != "" {
		if len(v) > 7 && strings.EqualFold(v[:7], "bearer ") {
			return strings.TrimSpace(v[7:])
		}
	}
	return strings.TrimSpace(r.Header.Get("X-Api-Key"))
}

// requireAPIKey authenticates, applies the per-key rate limit, and attaches
// the credential to the request context.
// recordRejected files a request that never reached the upstream.
//
// Its own helper because the fields are the interesting part: no key, no model,
// no tokens, and an address — which is the only identity a refused request has.
// Rejected is true, so every aggregate steps over it and only the request log
// shows it.
//
// Deliberately not called for the anonymous rate limiter's own refusal. That
// check runs before the key lookup precisely to keep a flood off the database,
// and writing a row there would hand back the cost it exists to avoid. A flood
// shows up as rejections up to the per-IP cap and then as nothing, which is
// what the cap means; the access log still records every one.
func (s *Gateway) recordRejected(r *http.Request, ip string, status int, reason string) {
	s.recorder.Record(store.UsageEvent{
		At:       time.Now(),
		Path:     r.URL.Path,
		Status:   status,
		Client:   api.ClientName(r.UserAgent()),
		IP:       ip,
		Rejected: true,
		Error:    reason,
	}, 0)
}

func (s *Gateway) requireAPIKey(next http.Handler) http.Handler {
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
			httpx.WriteError(w, http.StatusTooManyRequests, "rate_limit", "too many requests")
			return
		}

		cred := extractCredential(r)
		if cred == "" {
			anon()
			s.recordRejected(r, ip, http.StatusUnauthorized, "missing API key")
			httpx.WriteError(w, http.StatusUnauthorized, "authentication_error", "missing API key")
			return
		}

		key, err := s.store.Authenticate(r.Context(), cred)
		if errors.Is(err, store.ErrKeyNotFound) {
			anon()
			s.recordRejected(r, ip, http.StatusUnauthorized, "invalid API key")
			httpx.WriteError(w, http.StatusUnauthorized, "authentication_error", "invalid API key")
			return
		}
		if err != nil {
			s.log.Error("authenticate", "err", err, "request_id", httpx.RequestID(r.Context()))
			httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "internal server error")
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
			s.recorder.Record(store.UsageEvent{
				At: time.Now(), KeyID: key.ID, KeyName: key.Name,
				Path: r.URL.Path, Status: http.StatusTooManyRequests,
				Client: api.ClientName(r.UserAgent()), IP: ip, Rejected: true,
				Error: "rate limited by this gateway, not by the upstream",
			}, 0)
			httpx.WriteError(w, http.StatusTooManyRequests, "rate_limit", "too many requests")
			return
		}
		if !s.withinBudget(w, r, key) {
			return
		}

		s.store.TouchKey(r.Context(), key.ID)
		ctx := httpx.WithAPIKey(r.Context(), key)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
