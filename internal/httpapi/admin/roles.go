package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/store"
)

// RoleJSON is one model role as the admin API reports it, for the gateway or
// for one key.
type RoleJSON struct {
	Role string `json:"role"`
	// Model is what this scope binds it to: for the gateway, empty is
	// automatic (the newest of Family); for a key, empty is "the gateway's".
	Model  string `json:"model"`
	Family string `json:"family"`
	// Serving is what a request for the role gets now, and Problem why it
	// cannot be served, when it cannot.
	Serving string `json:"serving"`
	Problem string `json:"problem,omitempty"`
}

// handleModelRoles answers the roles for the gateway, or with ?key= for one
// key: what each is bound to there and what it serves now.
func (s *Admin) handleModelRoles(w http.ResponseWriter, r *http.Request) {
	keyID := strings.TrimSpace(r.URL.Query().Get("key"))
	var key store.APIKey
	if keyID != "" {
		k, err := s.store.Key(r.Context(), keyID)
		if errors.Is(err, store.ErrKeyNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "no such key")
			return
		}
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		key = k
	}
	all, err := s.store.ModelRoles(r.Context())
	if err != nil {
		s.log.Error("model roles", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not read the model roles")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out := make([]RoleJSON, 0, len(store.Roles))
	for _, role := range store.Roles {
		j := RoleJSON{Role: role, Family: store.RoleFamily(role)}
		for _, b := range all {
			if b.Role == role && b.KeyID == keyID {
				j.Model = b.Model
			}
		}
		j.Serving, j.Problem = s.roleTarget(ctx, key, role)
		out = append(out, j)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"roles": out})
}

// handleSetModelRole binds a role for the gateway, or with key_id for one key.
// An empty model is automatic for the gateway, and "follow the gateway" for a
// key. It applies from the next request.
func (s *Admin) handleSetModelRole(w http.ResponseWriter, r *http.Request) {
	var body struct {
		KeyID string `json:"key_id"`
		Role  string `json:"role"`
		Model string `json:"model"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if body.KeyID != "" {
		if _, err := s.store.Key(r.Context(), body.KeyID); errors.Is(err, store.ErrKeyNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "no such key")
			return
		}
	}
	if err := s.store.SetModelRole(r.Context(), body.KeyID, body.Role, body.Model); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	s.log.Warn("model role set", "role", body.Role, "key", body.KeyID, "model", body.Model,
		"ip", httpx.ClientIP(r.Context()))
	w.WriteHeader(http.StatusNoContent)
}

// handleKeyOfferedModels is the model list one key is offered — /v1/models as
// that key sees it, the roles first — for writing its configs.
func (s *Admin) handleKeyOfferedModels(w http.ResponseWriter, r *http.Request) {
	key, err := s.store.Key(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrKeyNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "no such key")
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	body, err := s.keyModels(ctx, key)
	if err != nil {
		s.log.Warn("could not read a key's model list", "err", err)
		httpx.WriteError(w, http.StatusBadGateway, "api_error", "could not reach the upstream model list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
