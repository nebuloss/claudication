package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

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

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

func writeError(w http.ResponseWriter, status int, kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{Error: errorDetail{Message: msg, Type: kind}})
}

// requireAPIKey authenticates, applies the per-key rate limit, and attaches
// the credential to the request context.
func (s *Server) requireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIPFrom(r.Context())

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
		anon := func() bool { return s.anonLimiter.allow(ip, s.cfg.Limits.AnonPerMinute) }
		if !s.anonLimiter.peek(ip, s.cfg.Limits.AnonPerMinute) {
			writeError(w, http.StatusTooManyRequests, "rate_limit", "too many requests")
			return
		}

		cred := extractCredential(r)
		if cred == "" {
			anon()
			writeError(w, http.StatusUnauthorized, "authentication_error", "missing API key")
			return
		}

		key, err := s.store.Authenticate(r.Context(), cred)
		if errors.Is(err, store.ErrKeyNotFound) {
			anon()
			writeError(w, http.StatusUnauthorized, "authentication_error", "invalid API key")
			return
		}
		if err != nil {
			s.log.Error("authenticate", "err", err, "request_id", requestIDFrom(r.Context()))
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		rpm := key.RPMLimit
		if rpm == 0 {
			rpm = s.cfg.Limits.RequestsPerMinute
		}
		if !s.keyLimiter.allow(key.ID, rpm) {
			writeError(w, http.StatusTooManyRequests, "rate_limit", "too many requests")
			return
		}
		if !s.withinBudget(w, r, key) {
			return
		}

		s.store.TouchKey(r.Context(), key.ID)
		ctx := context.WithValue(r.Context(), ctxKeyAPIKey, key)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
