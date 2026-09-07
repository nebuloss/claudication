package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/nebuloss/claudication/internal/store"
)

type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyAPIKey
	ctxKeyClientIP
)

func requestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRequestID).(string); ok {
		return v
	}
	return ""
}

// APIKeyFrom returns the credential that authenticated the request.
func APIKeyFrom(ctx context.Context) (store.APIKey, bool) {
	v, ok := ctx.Value(ctxKeyAPIKey).(store.APIKey)
	return v, ok
}

func clientIPFrom(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyClientIP).(string); ok {
		return v
	}
	return "unknown"
}

// statusRecorder captures the status code without buffering the body, so
// streaming responses stay unbuffered. Buffering here would stall Claude Code,
// which reads the stream as it arrives.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusRecorder) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer, which is
// what keeps Flush working through the wrapper. Without this, SSE would buffer.
func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) withRequestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idBytes := make([]byte, 8)
		_, _ = rand.Read(idBytes)
		id := hex.EncodeToString(idBytes)

		ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
		ctx = context.WithValue(ctx, ctxKeyClientIP, clientIP(r, s.trustedProxies))

		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic serving request",
					"panic", rec,
					"method", r.Method,
					"path", r.URL.Path,
					"request_id", requestIDFrom(r.Context()))
				// Headers may already be sent on a streaming response; only
				// write a status if nothing has gone out yet.
				if sr, ok := w.(*statusRecorder); ok && sr.wrote {
					return
				}
				writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"ip", clientIPFrom(r.Context()),
			"request_id", requestIDFrom(r.Context()),
		}
		// The gateway contract documents these as attribution headers for
		// gateways to consume, so cost can be attributed per session and per
		// subagent without ever parsing a request body.
		if v := r.Header.Get("X-Claude-Code-Session-Id"); v != "" {
			attrs = append(attrs, "cc_session", v)
		}
		if v := r.Header.Get("X-Claude-Code-Agent-Id"); v != "" {
			attrs = append(attrs, "cc_agent", v)
		}
		if key, ok := APIKeyFrom(r.Context()); ok {
			attrs = append(attrs, "api_key", key.Display(), "api_key_name", key.Name)
		}
		s.log.Info("request", attrs...)
	})
}

func (s *Server) withBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, s.cfg.Limits.MaxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}
