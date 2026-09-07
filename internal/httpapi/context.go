package httpapi

import (
	"context"
	"net/http"
	"time"
)

// contextWithTimeout bounds an upstream call without shortening a deadline the
// caller already imposed.
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	if deadline, ok := r.Context().Deadline(); ok && time.Until(deadline) < d {
		return context.WithCancel(r.Context())
	}
	return context.WithTimeout(r.Context(), d)
}
