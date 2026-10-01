package httpx

import (
	"context"
	"net/http"
	"time"

	"claudication/internal/store"
)

type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyAPIKey
	ctxKeyClientIP
)

func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRequestID).(string); ok {
		return v
	}
	return ""
}

// APIKey returns the credential that authenticated the request.
func APIKey(ctx context.Context) (store.APIKey, bool) {
	v, ok := ctx.Value(ctxKeyAPIKey).(store.APIKey)
	return v, ok
}

func ClientIP(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyClientIP).(string); ok {
		return v
	}
	return "unknown"
}

// WithAPIKey records the credential that authenticated the request.
func WithAPIKey(ctx context.Context, key store.APIKey) context.Context {
	return context.WithValue(ctx, ctxKeyAPIKey, key)
}

// TimeoutContext bounds an upstream call without shortening a deadline the
// caller already imposed.
func TimeoutContext(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	if deadline, ok := r.Context().Deadline(); ok && time.Until(deadline) < d {
		return context.WithCancel(r.Context())
	}
	return context.WithTimeout(r.Context(), d)
}
