package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/store"
)

// handleAccountModels lists the models one account's subscription serves,
// each with whether this gateway lets that account serve it.
//
// Asked of the upstream with that account's own token rather than read from
// a shared list: subscriptions differ in what they serve, and a switch is only
// meaningful over the models the account actually has. A model switched off
// that the upstream has since stopped listing is reported too, unlisted, so
// the switch can still be found and turned back on.
func (s *Admin) handleAccountModels(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	acct, err := s.store.Account(r.Context(), id)
	if errors.Is(err, store.ErrAccountNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "no such account")
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	body, err := s.accountModels(ctx, id, "limit=1000")
	if err != nil {
		s.log.Warn("could not read an account's models", "account", id, "err", err)
		httpx.WriteError(w, http.StatusBadGateway, "api_error",
			"could not read this account's models from the upstream: "+err.Error())
		return
	}
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "api_error", "the upstream model list was unreadable")
		return
	}

	listed := map[string]bool{}
	models := make([]map[string]any, 0, len(list.Data)+len(acct.ModelsOff))
	for _, m := range list.Data {
		mid, _ := m["id"].(string)
		if mid == "" {
			continue
		}
		listed[store.ModelKey(mid)] = true
		m["enabled"] = acct.Serves(mid)
		m["listed"] = true
		models = append(models, m)
	}
	for _, off := range acct.ModelsOff {
		if !listed[store.ModelKey(off)] {
			models = append(models, map[string]any{"id": off, "enabled": false, "listed": false})
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"account_id": id, "models": models})
}

// handleSetAccountModel turns one model on or off for one account.
//
// It takes effect on the next request: the pool reads the switches with the
// accounts it picks from. Turning a model off on the last account that has it
// on is allowed, and is how a model is withdrawn from the whole gateway.
func (s *Admin) handleSetAccountModel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model   string `json:"model"`
		Enabled *bool  `json:"enabled"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if body.Model == "" || body.Enabled == nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request",
			`expected {"model": "...", "enabled": true|false}`)
		return
	}
	id := r.PathValue("id")
	switch err := s.store.SetAccountModel(r.Context(), id, body.Model, *body.Enabled); {
	case errors.Is(err, store.ErrAccountNotFound):
		httpx.WriteError(w, http.StatusNotFound, "not_found", "no such account")
		return
	case err != nil:
		s.log.Error("set account model", "err", err, "account", id, "model", body.Model)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not store the switch")
		return
	}
	s.log.Warn("account model switched", "account", id, "model", body.Model,
		"enabled", *body.Enabled, "ip", httpx.ClientIP(r.Context()))

	acct, err := s.store.Account(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	off := acct.ModelsOff
	if off == nil {
		off = []string{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"account_id": id, "models_off": off})
}
