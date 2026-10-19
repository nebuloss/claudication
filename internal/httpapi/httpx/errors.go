// Package httpx is the HTTP plumbing every surface of the gateway shares:
// the error and JSON shapes, what the request context carries, the
// middleware around every listener, the caller's address, and reading a
// request body.
//
// It knows nothing about relaying, administration or the web UI, which is
// what lets each of those be a package of its own beside it.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// Kind is the type field of the gateway's own error envelope, the one the
// admin API and the web surface answer in. The client-facing APIs answer in
// their own dialect's envelope instead, with an api.ErrorKind.
//
// Typed so the common kinds are spelled once: the admin API answered the same
// failure as "api_error" from one handler and "internal_error" from the next.
// The UI acts on a few endpoint-specific kinds — wrong_account,
// secret_not_kept, needs_setup — which stay beside the handler that sends
// them.
type Kind string

const (
	// KindInvalidRequest is a request the handler cannot act on as sent.
	KindInvalidRequest Kind = "invalid_request"
	// KindNotFound is a thing that does not exist, or a path nothing serves.
	KindNotFound Kind = "not_found"
	// KindRateLimit is a caller over a limiter.
	KindRateLimit Kind = "rate_limit"
	// KindInternal is the gateway failing; see Internal.
	KindInternal Kind = "internal_error"
	// KindUpstream is the upstream failing, or answering with something that
	// could not be read, when the gateway asked on the operator's behalf.
	KindUpstream Kind = "upstream_error"
)

type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Message string `json:"message"`
	Type    Kind   `json:"type"`
}

func WriteError(w http.ResponseWriter, status int, kind Kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorBody{Error: ErrorDetail{Message: msg, Type: kind}})
}

// Internal answers a failure that is the gateway's own: logged in full, and
// answered as "could not <what>" with the error itself left out.
//
// Left out because what an error says is for the log. A store error names
// tables, columns and file paths, which tell an operator's browser nothing it
// can act on and tell anyone else something about the box. Seventeen admin
// handlers wrote err.Error() straight into the response; this is the one way
// to answer a 500 instead.
func Internal(w http.ResponseWriter, log *slog.Logger, what string, err error) {
	log.Error("could not "+what, "err", err)
	WriteError(w, http.StatusInternalServerError, KindInternal, "could not "+what)
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// DecodeJSON reads a JSON request body of up to 1 MiB into dst, answering 400
// and returning false when it cannot.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	return DecodeJSONLimit(w, r, dst, 1<<20, "")
}

// DecodeJSONLimit is DecodeJSON for a body that is never more than limit
// bytes: a switch, a title. usage is what the 400 says instead of the decoder's
// own error, so a caller who sent the wrong shape is told the right one.
func DecodeJSONLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64, usage string) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(dst); err != nil {
		msg := usage
		if msg == "" {
			msg = "could not read the request body: " + err.Error()
		}
		WriteError(w, http.StatusBadRequest, KindInvalidRequest, msg)
		return false
	}
	return true
}

// ── middleware ───────────────────────────────────────────────────────────
