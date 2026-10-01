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
	"net/http"
)

type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

func WriteError(w http.ResponseWriter, status int, kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorBody{Error: ErrorDetail{Message: msg, Type: kind}})
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(dst); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request", "could not read the request body: "+err.Error())
		return false
	}
	return true
}

// ── middleware ───────────────────────────────────────────────────────────
