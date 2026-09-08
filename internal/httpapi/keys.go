package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"claudication/internal/store"
)

// Client API keys, from the admin API.
//
// These were CLI-only until now, which made the UI useless for the thing the
// gateway is for: you cannot point Claude Code at a proxy without a key to
// give it.

type keyJSON struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Display    string `json:"display"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	RPMLimit   int    `json:"rpm_limit"`
	// Traffic over the reporting window, so a key nobody uses is visible.
	Requests int64 `json:"requests"`
	Tokens   int64 `json:"tokens"`
}

func toKeyJSON(k store.APIKey, use store.UsageBucket) keyJSON {
	out := keyJSON{
		ID:        k.ID,
		Name:      k.Name,
		Display:   k.Display(),
		CreatedAt: k.CreatedAt.UTC().Format(time.RFC3339),
		RPMLimit:  k.RPMLimit,
		Requests:  use.Requests,
		Tokens:    use.InputTokens + use.OutputTokens + use.CacheTokens,
	}
	if k.LastUsedAt != nil {
		out.LastUsedAt = k.LastUsedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.store.ListKeys(r.Context())
	if err != nil {
		s.log.Error("list api keys", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not list keys")
		return
	}

	// Usage is a nicety; a failure here should not hide the keys themselves.
	usage, err := s.store.KeyUsage(r.Context(), time.Now().Add(-s.cfg.Usage.Window()))
	if err != nil {
		s.log.Warn("key usage unavailable", "err", err)
		usage = map[string]store.UsageBucket{}
	}

	out := make([]keyJSON, 0, len(keys))
	for _, k := range keys {
		out = append(out, toKeyJSON(k, usage[k.ID]))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"keys":        out,
		"window_days": int(s.cfg.Usage.Window().Hours() / 24),
	})
}

// handleCreateKey mints a key and returns the plaintext — once, here, and
// never again. The store keeps only sha256(key) and a lookup prefix.
func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		RPMLimit int    `json:"rpm_limit"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "a name is required")
		return
	}
	if body.RPMLimit < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request",
			"rpm_limit cannot be negative (0 means the global default)")
		return
	}

	// token_budget is left at 0 deliberately: the column exists but nothing
	// enforces it yet, and offering a limit that does not limit is worse than
	// not offering one.
	key, plaintext, err := s.store.CreateKey(r.Context(), body.Name, body.RPMLimit, 0)
	if err != nil {
		s.log.Error("create api key", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not create the key")
		return
	}

	s.log.Info("api key created", "id", key.ID, "name", key.Name,
		"ip", clientIPFrom(r.Context()))
	writeJSON(w, http.StatusOK, map[string]any{
		"key":       toKeyJSON(key, store.UsageBucket{}),
		"plaintext": plaintext,
	})
}

// handleDeleteKey withdraws a key. There is no revoke beside it: revocation
// could not be undone either, so it was this under another name.
// handleUpdateKey renames a key or changes its rate limit. The secret is not
// touched, so nothing that already holds the key has to be told anything.
func (s *Server) handleUpdateKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		RPMLimit int    `json:"rpm_limit"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "a name is required")
		return
	}
	if body.RPMLimit < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request",
			"rpm_limit cannot be negative (0 means the global default)")
		return
	}

	id := r.PathValue("id")
	switch err := s.store.UpdateKey(r.Context(), id, body.Name, body.RPMLimit); {
	case errors.Is(err, store.ErrKeyNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such key")
		return
	case err != nil:
		s.log.Error("update api key", "err", err, "id", id)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not update the key")
		return
	}
	s.log.Info("api key updated", "id", id, "name", body.Name, "ip", clientIPFrom(r.Context()))

	keys, err := s.store.ListKeys(r.Context())
	if err == nil {
		for _, k := range keys {
			if k.ID == id {
				writeJSON(w, http.StatusOK, map[string]any{"key": toKeyJSON(k, store.UsageBucket{})})
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	switch err := s.store.DeleteKey(r.Context(), id); {
	case errors.Is(err, store.ErrKeyNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such key")
		return
	case err != nil:
		s.log.Error("delete api key", "err", err, "id", id)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not delete the key")
		return
	}
	s.log.Info("api key deleted", "id", id, "ip", clientIPFrom(r.Context()))
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
