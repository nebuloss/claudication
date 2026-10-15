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

// VirtualModelJSON is one virtual model in one scope, as the admin API
// reports it.
type VirtualModelJSON struct {
	// KeyID and Client are its scope: both empty for the gateway-wide one.
	KeyID       string   `json:"key_id"`
	Client      string   `json:"client"`
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Targets     []string `json:"targets"`
	// Serving is the target a request would get now — the first an account
	// has on and, for a key's mapping, that key may use — or empty when none.
	Serving   string `json:"serving"`
	UpdatedAt string `json:"updated_at"`
}

// handleVirtualModels lists every virtual model in every scope. The admin UI
// shows the gateway-wide ones on the Models screen and a key's on its page.
func (s *Admin) handleVirtualModels(w http.ResponseWriter, r *http.Request) {
	all, err := s.store.VirtualModels(r.Context())
	if err != nil {
		s.log.Error("virtual models", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not read the virtual models")
		return
	}
	keys := map[string]store.APIKey{}
	out := make([]VirtualModelJSON, 0, len(all))
	for _, v := range all {
		var key store.APIKey
		if v.KeyID != "" {
			k, ok := keys[v.KeyID]
			if !ok {
				k, _ = s.store.Key(r.Context(), v.KeyID)
				keys[v.KeyID] = k
			}
			key = k
		}
		out = append(out, s.virtualJSON(r.Context(), v, key))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"models": out})
}

func (s *Admin) virtualJSON(ctx context.Context, v store.VirtualModel, key store.APIKey) VirtualModelJSON {
	serving := ""
	for _, t := range v.Targets {
		if key.Allows(t) && s.pool.Offers(ctx, "anthropic", t) {
			serving = t
			break
		}
	}
	return VirtualModelJSON{
		KeyID: v.KeyID, Client: v.Client, Name: v.Name, DisplayName: v.DisplayName,
		Targets: v.Targets, Serving: serving,
		UpdatedAt: v.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// handlePutVirtualModel creates or replaces a virtual model in its scope. It
// applies from the next request: the gateway reads the mapping per request.
func (s *Admin) handlePutVirtualModel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		KeyID       string   `json:"key_id"`
		Client      string   `json:"client"`
		Name        string   `json:"name"`
		DisplayName string   `json:"display_name"`
		Targets     []string `json:"targets"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	v := store.VirtualModel{
		KeyID: strings.TrimSpace(body.KeyID), Client: strings.TrimSpace(body.Client),
		Name: strings.TrimSpace(body.Name), DisplayName: body.DisplayName, Targets: body.Targets,
	}
	var key store.APIKey
	if v.KeyID != "" {
		k, err := s.store.Key(r.Context(), v.KeyID)
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
	if err := v.Validate(); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := s.store.PutVirtualModel(r.Context(), v); err != nil {
		s.log.Error("put virtual model", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not store the virtual model")
		return
	}
	s.log.Warn("virtual model set", "name", v.Name, "key", v.KeyID, "client", v.Client,
		"targets", strings.Join(v.Targets, ","), "ip", httpx.ClientIP(r.Context()))
	v.UpdatedAt = time.Now()
	httpx.WriteJSON(w, http.StatusOK, s.virtualJSON(r.Context(), v, key))
}

// handleDeleteVirtualModel removes one mapping from one scope, named by
// ?key_id=&client=&name=. Removing a key's or an app's mapping lets the wider
// one apply again.
func (s *Admin) handleDeleteVirtualModel(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	switch err := s.store.DeleteVirtualModel(r.Context(), q.Get("key_id"), q.Get("client"), q.Get("name")); {
	case errors.Is(err, store.ErrVirtualModelNotFound):
		httpx.WriteError(w, http.StatusNotFound, "not_found", "no such virtual model in that scope")
		return
	case err != nil:
		s.log.Error("delete virtual model", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not delete it")
		return
	}
	s.log.Warn("virtual model removed", "name", q.Get("name"), "key", q.Get("key_id"),
		"client", q.Get("client"), "ip", httpx.ClientIP(r.Context()))
	w.WriteHeader(http.StatusNoContent)
}

// handleKeyClientModels is the model list one key's client app is offered —
// /v1/models as that app on that key would see it — for writing its config.
func (s *Admin) handleKeyClientModels(w http.ResponseWriter, r *http.Request) {
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
	body, err := s.keyModels(ctx, key, r.URL.Query().Get("client"))
	if err != nil {
		s.log.Warn("could not read a key's model list", "err", err)
		httpx.WriteError(w, http.StatusBadGateway, "api_error", "could not reach the upstream model list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
