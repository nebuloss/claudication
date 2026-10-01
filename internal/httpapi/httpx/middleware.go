package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"time"
)

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

// WithRequestContext gives every request an id and resolves who made it,
// believing X-Forwarded-For only from a trusted proxy.
func WithRequestContext(next http.Handler, trusted []*net.IPNet) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idBytes := make([]byte, 8)
		_, _ = rand.Read(idBytes)
		id := hex.EncodeToString(idBytes)

		ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
		ctx = context.WithValue(ctx, ctxKeyClientIP, ResolveClientIP(r, trusted))

		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// WithRecovery turns a panic into a 500, or into nothing once a stream has
// started, so one bad request cannot take the process down.
func WithRecovery(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("panic serving request",
					"panic", rec,
					"method", r.Method,
					"path", r.URL.Path,
					"request_id", RequestID(r.Context()))
				// Headers may already be sent on a streaming response; only
				// write a status if nothing has gone out yet.
				if sr, ok := w.(*statusRecorder); ok && sr.wrote {
					return
				}
				WriteError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// WithAccessLog writes one line per request.
func WithAccessLog(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"ip", ClientIP(r.Context()),
			"request_id", RequestID(r.Context()),
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
		if key, ok := APIKey(r.Context()); ok {
			attrs = append(attrs, "api_key", key.Display(), "api_key_name", key.Name)
		}
		log.Info("request", attrs...)
	})
}

// WithBodyLimit caps how much of a request body is read.
func WithBodyLimit(next http.Handler, limit int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}
