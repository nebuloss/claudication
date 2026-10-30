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
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such account")
		return
	}
	if err != nil {
		httpx.Internal(w, s.log, "read the account", err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	// The list the gateway last read, so a page load, and the page's reads
	// while the account's models are checked, call no upstream; ?fresh=1
	// reads it again.
	body, err := s.models.AccountList(ctx, id, r.URL.Query().Get("fresh") == "1")
	if err != nil {
		s.log.Warn("could not read an account's models", "account", id, "err", err)
		httpx.WriteError(w, http.StatusBadGateway, httpx.KindUpstream,
			"could not read this account's models from the upstream: "+err.Error())
		return
	}
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		httpx.WriteError(w, http.StatusBadGateway, httpx.KindUpstream, "the upstream model list was unreadable")
		return
	}

	// Each model's last test, beside its switch. A nicety: without it the
	// models still list.
	tests, err := s.store.ModelTests(ctx, id)
	if err != nil {
		s.log.Warn("could not read the account's model tests", "account", id, "err", err)
	}
	// Which of the offs are the gateway's own, for its row to say so.
	auto, err := s.store.AutoOffModels(ctx, id)
	if err != nil {
		s.log.Warn("could not read the account's switched-off models", "account", id, "err", err)
	}
	autoOff := func(model string) bool {
		for m := range auto {
			if store.ModelKey(m) == store.ModelKey(model) {
				return true
			}
		}
		return false
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
		if !acct.Serves(mid) && autoOff(mid) {
			m["auto_off"] = true
		}
		m["listed"] = true
		if t, ok := tests[mid]; ok {
			m["test"] = t
		}
		models = append(models, m)
	}
	for _, off := range acct.ModelsOff {
		if !listed[store.ModelKey(off)] {
			models = append(models, map[string]any{"id": off, "enabled": false, "listed": false, "auto_off": autoOff(off)})
		}
	}
	// scanning says the models are being checked now, for the page to read
	// again until they are.
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"account_id": id, "models": models,
		"scanning": s.availability.Scanning(id)})
}

// handleKeyModels lists the models a key could ask for — every model this
// gateway serves, the same list /v1/models answers — each with whether the
// key may. For a client's key, only what is available to that client now
// (internal/service/models, available.go): a direct-mode client is shown no
// model it would be refused. For the gateway's own key, a model kept off it
// that the gateway no longer serves is listed too, unlisted, so the switch
// can still be found.
func (s *Admin) handleKeyModels(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	key, err := s.store.Key(r.Context(), id)
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
	// For a client, what a direct-mode client is offered, read as if
	// everything were allowed: these are the switches that say what is, and
	// a model switched off has to stay here to be switched back on. In a
	// provider offered by family that is its families (claude-opus), the
	// switch being the family's: what the client's names may follow, and
	// what it may name.
	client := key.ClientType != ""
	var body []byte
	if client {
		body, err = s.models.Offered(ctx, nil)
	} else {
		body, err = s.models.List(ctx)
	}
	if err != nil {
		s.log.Warn("could not read the gateway's models", "err", err)
		httpx.WriteError(w, http.StatusBadGateway, httpx.KindUpstream,
			"could not read the models the accounts serve: "+err.Error())
		return
	}
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		httpx.WriteError(w, http.StatusBadGateway, httpx.KindUpstream, "the model list was unreadable")
		return
	}

	listed := map[string]bool{}
	models := make([]map[string]any, 0, len(list.Data)+len(key.ModelsOff))
	for _, m := range list.Data {
		mid, _ := m["id"].(string)
		if mid == "" {
			continue
		}
		listed[store.ModelKey(mid)] = true
		m["enabled"] = key.Allows(mid)
		m["listed"] = true
		models = append(models, m)
	}
	// A client's switch over a model it is not offered would switch nothing
	// it can name; it comes back, as it was, when the model does.
	for _, off := range key.ModelsOff {
		if !client && !listed[store.ModelKey(off)] {
			models = append(models, map[string]any{"id": off, "enabled": false, "listed": false})
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"key_id": id, "models": models})
}

// handleSetKeyModel allows or forbids one model for one key, from the next
// request on: keys are read afresh on every request.
func (s *Admin) handleSetKeyModel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model   string `json:"model"`
		Enabled *bool  `json:"enabled"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if body.Model == "" || body.Enabled == nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest,
			`expected {"model": "...", "enabled": true|false}`)
		return
	}
	id := r.PathValue("id")
	switch err := s.store.SetKeyModel(r.Context(), id, body.Model, *body.Enabled); {
	case errors.Is(err, store.ErrKeyNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such key")
		return
	case err != nil:
		s.log.Error("set key model", "err", err, "key", id, "model", body.Model)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not store the switch")
		return
	}
	s.log.Warn("key model switched", "key", id, "model", body.Model,
		"enabled", *body.Enabled, "ip", httpx.ClientIP(r.Context()))

	key, err := s.store.Key(r.Context(), id)
	if err != nil {
		httpx.Internal(w, s.log, "read the key", err)
		return
	}
	off := key.ModelsOff
	if off == nil {
		off = []string{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"key_id": id, "models_off": off})
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
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest,
			`expected {"model": "...", "enabled": true|false}`)
		return
	}
	id := r.PathValue("id")
	switch err := s.store.SetAccountModel(r.Context(), id, body.Model, *body.Enabled); {
	case errors.Is(err, store.ErrAccountNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such account")
		return
	case err != nil:
		s.log.Error("set account model", "err", err, "account", id, "model", body.Model)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not store the switch")
		return
	}
	s.log.Warn("account model switched", "account", id, "model", body.Model,
		"enabled", *body.Enabled, "ip", httpx.ClientIP(r.Context()))

	acct, err := s.store.Account(r.Context(), id)
	if err != nil {
		httpx.Internal(w, s.log, "read the account", err)
		return
	}
	off := acct.ModelsOff
	if off == nil {
		off = []string{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"account_id": id, "models_off": off})
}
