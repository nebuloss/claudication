package admin

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"claudication/internal/api"
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
	// Clients are its per-client bindings, on the gateway's view only: a
	// client binding is the gateway's, and a key's view is about the key.
	Clients []ClientRoleJSON `json:"clients,omitempty"`
}

// ClientRoleJSON is one alias's binding for one client, and what that client
// is served for it now by a key that binds nothing itself.
type ClientRoleJSON struct {
	Client  string `json:"client"`
	Model   string `json:"model"`
	Serving string `json:"serving"`
	Problem string `json:"problem,omitempty"`
}

// handleModelRoles answers the aliases for the gateway, or with ?key= for one
// key: the built-in ones first, then the custom ones, each with what it is
// bound to there and what it serves now. The gateway's view also has each
// alias's client bindings, and the clients one could be made for.
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
	aliases, err := s.models.Aliases(ctx)
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
			if b.Role != a.Name {
				continue
			}
			switch {
			case b.Client == "" && b.KeyID == keyID:
				j.Model = b.Model
			case b.Client != "" && keyID == "":
				c := ClientRoleJSON{Client: b.Client, Model: b.Model}
				c.Serving, c.Problem = s.models.RoleTarget(ctx, key, b.Client, a.Name)
				j.Clients = append(j.Clients, c)
			}
		}
		j.Serving, j.Problem = s.models.RoleTarget(ctx, key, "", a.Name)
		out = append(out, j)
	}
	if keyID != "" {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"roles": out})
		return
	}
	clients, err := s.bindableClients(ctx, all)
	if err != nil {
		httpx.Internal(w, s.log, "read the clients", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"roles": out, "clients": clients})
}

// bindableClients are the clients an alias can be bound for, as the admin UI
// offers them: the ones the gateway recognises, which a fresh gateway has not
// yet seen, then any other the request log has recorded or a binding names.
func (s *Admin) bindableClients(ctx context.Context, all []store.ModelRole) ([]string, error) {
	seen, err := s.store.SeenClients(ctx)
	if err != nil {
		return nil, err
	}
	out := api.KnownClients()
	for _, r := range all {
		if r.Client != "" {
			seen = append(seen, r.Client)
		}
	}
	for _, c := range seen {
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out, nil
}

// handleSetModelRole binds an alias, for the gateway, with key_id for one
// key, or with client for one client. Binding a new name on the gateway
// creates a custom alias. An empty model lets a built-in follow its family
// again, or a key or a client follow the gateway. It applies from the next
// request.
func (s *Admin) handleSetModelRole(w http.ResponseWriter, r *http.Request) {
	var body struct {
		KeyID  string `json:"key_id"`
		Client string `json:"client"`
		Role   string `json:"role"`
		Model  string `json:"model"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if body.KeyID != "" && body.Client != "" {
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest,
			"an alias is bound for a key or for a client, not for both")
		return
	}
	if body.KeyID != "" {
		if _, err := s.store.Key(r.Context(), body.KeyID); errors.Is(err, store.ErrKeyNotFound) {
			httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such key")
			return
		}
	}
	if body.KeyID != "" || body.Client != "" {
		// A key or a client binds an alias the gateway has, not a name of
		// its own.
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
	var err error
	if body.Client != "" {
		err = s.store.SetClientRole(r.Context(), body.Client, body.Role, body.Model)
	} else {
		err = s.store.SetModelRole(r.Context(), body.KeyID, body.Role, body.Model)
	}
	switch {
	case errors.Is(err, store.ErrInvalidRole):
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, err.Error())
		return
	case errors.Is(err, store.ErrKeyNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such api key")
		return
	case err != nil:
		httpx.Internal(w, s.log, "set the alias", err)
		return
	}
	s.log.Warn("model alias set", "alias", body.Role, "key", body.KeyID, "client", body.Client, "model", body.Model,
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
// that key sees it, the aliases first — for writing its configs. With
// ?client= it is the list as that client sees it, so a client whose aliases
// are bound apart has its config written for the models it will get.
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
	body, err := s.models.For(ctx, key, strings.TrimSpace(r.URL.Query().Get("client")))
	if err != nil {
		s.log.Warn("could not read a key's model list", "err", err)
		httpx.WriteError(w, http.StatusBadGateway, httpx.KindUpstream, "could not reach the upstream model list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
