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

// RoleJSON is one model alias as the admin API reports it, for the gateway or
// for one key.
type RoleJSON struct {
	Role string `json:"role"`
	// Model is what this scope binds it to: for the gateway, empty is
	// following the newest of Family; for a key, empty is "the gateway's".
	Model string `json:"model"`
	// Family is what a built-in alias follows while unbound; empty for a
	// custom one.
	Family  string `json:"family"`
	Builtin bool   `json:"builtin"`
	// Serving is what a request for the alias gets now, and Problem why it
	// cannot be served, when it cannot.
	Serving string `json:"serving"`
	Problem string `json:"problem,omitempty"`
}

// handleModelRoles answers the aliases for the gateway, or with ?key= for one
// key: the built-in ones first, then the custom ones, each with what it is
// bound to there and what it serves now.
func (s *Admin) handleModelRoles(w http.ResponseWriter, r *http.Request) {
	keyID := strings.TrimSpace(r.URL.Query().Get("key"))
	var key store.APIKey
	if keyID != "" {
		k, err := s.store.Key(r.Context(), keyID)
		if errors.Is(err, store.ErrKeyNotFound) {
			httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such key")
			return
		}
		if err != nil {
			httpx.Internal(w, s.log, "read the key", err)
			return
		}
		key = k
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	aliases, err := s.aliases(ctx)
	if err != nil {
		s.log.Error("model aliases", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not read the model aliases")
		return
	}
	all, err := s.store.ModelRoles(ctx)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not read the model aliases")
		return
	}
	out := make([]RoleJSON, 0, len(aliases))
	for _, a := range aliases {
		j := RoleJSON{Role: a.Name, Family: a.Family, Builtin: a.Builtin}
		for _, b := range all {
			if b.Role == a.Name && b.KeyID == keyID {
				j.Model = b.Model
			}
		}
		j.Serving, j.Problem = s.roleTarget(ctx, key, a.Name)
		out = append(out, j)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"roles": out})
}

// handleSetModelRole binds an alias, for the gateway or with key_id for one
// key. Binding a new name on the gateway creates a custom alias. An empty
// model lets a built-in follow its family again, or a key follow the gateway.
// It applies from the next request.
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
			httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such key")
			return
		}
		// A key binds an alias the gateway has, not a name of its own.
		if store.AliasFamily(body.Role) == "" {
			custom, err := s.store.CustomAliases(r.Context())
			if err != nil {
				httpx.Internal(w, s.log, "read the aliases", err)
				return
			}
			known := false
			for _, c := range custom {
				known = known || c == store.ModelKey(body.Role)
			}
			if !known {
				httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest,
					"no such alias; create it on the Models tab first")
				return
			}
		}
	}
	if err := s.store.SetModelRole(r.Context(), body.KeyID, body.Role, body.Model); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, err.Error())
		return
	}
	s.log.Warn("model alias set", "alias", body.Role, "key", body.KeyID, "model", body.Model,
		"ip", httpx.ClientIP(r.Context()))
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteAlias removes a custom alias, from the gateway and every key.
// Clients still configured with it are then asking for a model nobody serves.
func (s *Admin) handleDeleteAlias(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	switch err := s.store.DeleteAlias(r.Context(), name); {
	case errors.Is(err, store.ErrBuiltinAlias):
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, err.Error())
		return
	case errors.Is(err, store.ErrVirtualModelNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such alias")
		return
	case err != nil:
		httpx.Internal(w, s.log, "delete the alias", err)
		return
	}
	s.log.Warn("model alias deleted", "alias", name, "ip", httpx.ClientIP(r.Context()))
	w.WriteHeader(http.StatusNoContent)
}

// handleKeyOfferedModels is the model list one key is offered — /v1/models as
// that key sees it, the aliases first — for writing its configs.
func (s *Admin) handleKeyOfferedModels(w http.ResponseWriter, r *http.Request) {
	key, err := s.store.Key(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrKeyNotFound) {
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such key")
		return
	}
	if err != nil {
		httpx.Internal(w, s.log, "read the key", err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	body, err := s.keyModels(ctx, key)
	if err != nil {
		s.log.Warn("could not read a key's model list", "err", err)
		httpx.WriteError(w, http.StatusBadGateway, httpx.KindUpstream, "could not reach the upstream model list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
