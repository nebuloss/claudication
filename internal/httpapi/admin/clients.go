package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/service/clientconfig"
	"claudication/internal/service/clients"
	"claudication/internal/service/install"
	"claudication/internal/service/limits"
	modelsvc "claudication/internal/service/models"
	"claudication/internal/store"
)

// Clients: the things the operator set up to use the gateway, each holding
// one key. See internal/service/clients for the types and their slots, and
// clients in the store's schema.sql for why a client's id is its key's.
//
// The key's own endpoints (/admin/keys/{id}/...: rotate, secret, limits,
// models, install, delete) are the client's too, under the same id; these are
// what a client has beyond its key.

// ClientJSON is one client as the admin API reports it: its key's row, and
// what makes it a client.
type ClientJSON struct {
	KeyJSON
	Type     string       `json:"type"`
	Mode     clients.Mode `json:"mode"`
	Disabled bool         `json:"disabled"`
	// NameChats is whether its chats are named: its "Name chats" switch, or
	// always for a type that names its own and so has none (clients.Type,
	// NamesItsChats), whatever an older release stored for it.
	NameChats bool `json:"name_chats"`
	// FitImages is its "Shrink images" switch: whether a request of its
	// carrying more than 20 images has each capped at 2000 px when Claude
	// serves it (internal/relay/passes, ShrinkImages).
	FitImages bool `json:"fit_images"`
	// AgentManage is its "Agent may manage this client" switch: whether its
	// own agent may change it through the MCP server (internal/httpapi/agent).
	AgentManage bool `json:"agent_manage"`
	// DisabledAt is when it was switched off, when it is.
	DisabledAt string `json:"disabled_at,omitempty"`
	// ConfiguredAt is when it was first set up with its key; from then on
	// the key and the install command are not offered (refuseConfigured).
	ConfiguredAt string `json:"configured_at,omitempty"`
	// Install is its install link's state, polled while its page is open.
	Install install.State `json:"install"`
	// Update is its update link's state, once it is configured: the command
	// that rewrites its model names on a machine set up already, keeping the
	// key there (handleKeyUpdate).
	Update install.State `json:"update"`
	Slots  []SlotJSON    `json:"slots"`
	// Choices are what its slots can be bound to now: only what is
	// available to it (internal/service/models, available.go), so its page
	// offers nothing that would be refused. On its own page, not the list.
	Choices []modelsvc.Choice `json:"choices,omitempty"`
}

// SlotJSON is one of a client's slots: what its type says of it, its model
// name, what it is bound to, and what that serves now.
type SlotJSON struct {
	clients.Slot
	// Name is the model name the client asks for.
	Name string `json:"name"`
	// Model is the real model it is bound to, or Bound the target it follows
	// (the spec's Follows is the default it started on).
	Model string         `json:"model"`
	Bound clients.Follow `json:"bound,omitempty"`
	// Title names the binding as the slot's menu does.
	Title string `json:"title"`
	// Serving is the real model its binding points at now and Provider whose
	// accounts that is, shown even when it cannot be served.
	Serving  string `json:"serving"`
	Provider string `json:"provider,omitempty"`
	// Unavailable says why the binding is not available to this client now —
	// no account lists it, or it is switched off on every one that does — and
	// is empty when it is. Such a binding is kept and shown, and replaced only
	// by one of Choices.
	Unavailable string `json:"unavailable,omitempty"`
	// Removable is a slot the operator may take away: one of their own, on a
	// type that has them (clients.Type, Removable). Said here so the page
	// does not decide it a second time.
	Removable bool `json:"removable"`
}

// clientJSON is a client as the admin API reports it, its slots resolved.
func (s *Admin) clientJSON(ctx context.Context, c store.Client, key KeyJSON) ClientJSON {
	out := ClientJSON{KeyJSON: key, Type: c.Type, Mode: c.Mode, Disabled: c.DisabledAt != nil,
		NameChats: c.NameChats || c.Spec().NamesItsChats,
		FitImages: c.FitImages, AgentManage: c.AgentManage,
		Slots: make([]SlotJSON, 0, len(c.Slots))}
	if c.DisabledAt != nil {
		out.DisabledAt = c.DisabledAt.UTC().Format(time.RFC3339)
	}
	if c.ConfiguredAt != nil {
		out.ConfiguredAt = c.ConfiguredAt.UTC().Format(time.RFC3339)
	}
	out.Install = s.install.StateOf(c.ID)
	out.Update = s.install.UpdateStateOf(c.ID)
	t := c.Spec()
	for _, sl := range c.Slots {
		spec, _ := t.SlotFor(sl.Slot)
		if _, typed := c.Spec().Slot(sl.Slot); !typed {
			// An operator's own slot: its id is made up, its name is what
			// the operator knows it by.
			spec.Label = sl.Name
		}
		b, why := s.models.Resolve(ctx, c.ModelsOff, store.Binding{Model: sl.Model, Follows: sl.Follows})
		out.Slots = append(out.Slots, SlotJSON{Slot: spec, Name: sl.Name, Model: sl.Model, Bound: sl.Follows,
			Title: b.Title, Serving: b.Serving, Provider: b.Provider, Unavailable: why,
			Removable: t.Removable(sl.Slot)})
	}
	return out
}

// handleClientTypes is the table of client types — each type's slots and the
// API it speaks — and how each API is named. The UI builds the add-a-client
// picker and every slot's label from it, so the two cannot disagree. What a
// slot can be bound to is on each client (ClientJSON.Choices), since it
// changes with the accounts.
func (s *Admin) handleClientTypes(w http.ResponseWriter, r *http.Request) {
	type dialectJSON struct {
		ID    clients.Dialect `json:"id"`
		Title string          `json:"title"`
	}
	dialects := []dialectJSON{}
	for _, d := range []clients.Dialect{clients.Messages, clients.Responses} {
		dialects = append(dialects, dialectJSON{ID: d, Title: d.Title()})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"types": clients.All, "dialects": dialects,
		// So the page names the MCP server's address from the one copy of it.
		"mcp_path": clientconfig.MCPPath})
}

// handleListClients lists every client with its key's figures and its
// slots.
func (s *Admin) handleListClients(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	all, err := s.store.Clients(ctx)
	if err != nil {
		httpx.Internal(w, s.log, "list the clients", err)
		return
	}
	stats := s.keyStats(ctx)
	out := make([]ClientJSON, 0, len(all))
	for _, c := range all {
		out = append(out, s.clientJSON(ctx, c, stats.of(s, c.APIKey)))
	}
	httpx.WriteJSON(w, http.StatusOK, s.keyListEnvelope("clients", out))
}

// loadClient reads the client a path names, answering itself when it cannot.
func (s *Admin) loadClient(w http.ResponseWriter, r *http.Request) (store.Client, bool) {
	c, err := s.store.Client(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrClientNotFound) {
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such client")
		return store.Client{}, false
	}
	if err != nil {
		httpx.Internal(w, s.log, "read the client", err)
		return store.Client{}, false
	}
	return c, true
}

// writeClient answers one client, read afresh.
func (s *Admin) writeClient(w http.ResponseWriter, r *http.Request, id string, extra map[string]any) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	c, err := s.store.Client(ctx, id)
	if err != nil {
		httpx.Internal(w, s.log, "read the client", err)
		return
	}
	j := s.clientJSON(ctx, c, s.keyStats(ctx, c.ID).of(s, c.APIKey))
	j.Choices = s.models.Choices(ctx, c.ModelsOff)
	out := map[string]any{"client": j}
	for k, v := range extra {
		out[k] = v
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Admin) handleGetClient(w http.ResponseWriter, r *http.Request) {
	if c, ok := s.loadClient(w, r); ok {
		s.writeClient(w, r, c.ID, nil)
	}
}

// createClient makes a client of a type, with the type's slots under its
// names, each following its default target. The type is the client's for
// good, and the API it speaks is its type's.
func (s *Admin) createClient(ctx context.Context, name string, t clients.Type,
	lim store.KeyLimits) (store.Client, string, error) {
	initial := t.Initial()
	slots := make([]store.NewSlot, 0, len(initial))
	for _, a := range initial {
		slots = append(slots, store.NewSlot{Slot: a.Slot, Name: a.Name, Binding: store.Binding{Follows: a.Follows}})
	}
	return s.store.CreateClient(ctx, name, t.ID, lim, slots)
}

// handleCreateClient makes a client and its key, and answers the key's
// plaintext once, as creating a key does.
func (s *Admin) handleCreateClient(w http.ResponseWriter, r *http.Request) {
	var body struct {
		keyLimitsBody
		Type string `json:"type"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	t, ok := clients.Find(body.Type)
	switch {
	case name == "":
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, "a name is required")
		return
	case !ok:
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, "no such client type")
		return
	}
	lim := store.KeyLimits{RPMLimit: body.RPMLimit, RatePeriod: time.Duration(body.RatePeriodS) * time.Second,
		TokenBudget: body.TokenBudget}
	c, plaintext, err := s.createClient(r.Context(), name, t, lim)
	if s.clientError(w, err, "create the client") {
		return
	}
	s.log.Info("client created", "id", c.ID, "name", c.Name, "type", c.Type,
		"ip", httpx.ClientIP(r.Context()))
	s.writeClient(w, r, c.ID, map[string]any{"plaintext": plaintext})
}

// clientError answers a store error of a client's, and reports whether it
// did.
func (s *Admin) clientError(w http.ResponseWriter, err error, what string) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrInvalidClient):
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, err.Error())
	case errors.Is(err, store.ErrClientNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such client or slot")
	default:
		httpx.Internal(w, s.log, what, err)
	}
	return true
}

// slotBody is a slot's name and binding as the UI sends them: a model or a
// target to follow.
type slotBody struct {
	Name    string         `json:"name"`
	Model   string         `json:"model"`
	Follows clients.Follow `json:"follows"`
}

// handleSetSlot renames one slot and binds it.
func (s *Admin) handleSetSlot(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadClient(w, r)
	if !ok {
		return
	}
	var body slotBody
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	slot := r.PathValue("slot")
	b := modelsvc.Normal(store.Binding{Model: body.Model, Follows: body.Follows})
	// A binding the slot already has may stay, available or not — a rename
	// sends it back unchanged — but a new one has to be available to the
	// client, as its page offers: the API is held to what the page shows.
	if !boundTo(c, slot, b) && !s.available(w, r, c, b) {
		return
	}
	if s.clientError(w, s.store.SetSlot(r.Context(), c.ID, slot, body.Name, b), "set the slot") {
		return
	}
	s.log.Info("client slot set", "id", c.ID, "slot", slot, "name", body.Name, "model", body.Model,
		"follows", body.Follows, "ip", httpx.ClientIP(r.Context()))
	s.writeClient(w, r, c.ID, nil)
}

// handleAddSlot gives a client a model name of the operator's own, on a type
// that takes them (a custom client, or a typed one for its picker), following
// the newest Claude Opus unless a binding is given.
func (s *Admin) handleAddSlot(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadClient(w, r)
	if !ok {
		return
	}
	var body slotBody
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	// The MCP server's add_model holds a new name to the same rule.
	b, choice, why := s.models.NewSlot(r.Context(), c.ModelsOff, store.Binding{Model: body.Model, Follows: body.Follows})
	if why != "" {
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest,
			choice.Title+" is not available to this client: "+why)
		return
	}
	if s.clientError(w, s.store.AddSlot(r.Context(), c.ID, strings.TrimSpace(body.Name), b), "add the slot") {
		return
	}
	s.writeClient(w, r, c.ID, nil)
}

// boundTo reports whether a client's slot is bound to b already.
func boundTo(c store.Client, slot string, b store.Binding) bool {
	for _, sl := range c.Slots {
		if sl.Slot == slot {
			return sl.Model == strings.TrimSpace(b.Model) && sl.Follows == b.Follows
		}
	}
	return false
}

// available refuses a binding the client cannot be offered now, saying why,
// and reports whether it may be made.
func (s *Admin) available(w http.ResponseWriter, r *http.Request, c store.Client, b store.Binding) bool {
	if b.Follows != "" && !b.Follows.Valid() {
		// The store says what is wrong with it.
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	choice, why := s.models.Bindable(ctx, c.ModelsOff, store.Binding{
		Model: strings.TrimSpace(b.Model), Follows: b.Follows})
	if why == "" {
		return true
	}
	httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest,
		choice.Title+" is not available to this client: "+why)
	return false
}

// handleSetClientMode puts a custom client in binding or direct mode.
func (s *Admin) handleSetClientMode(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadClient(w, r)
	if !ok {
		return
	}
	var body struct {
		Mode clients.Mode `json:"mode"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if s.clientError(w, s.store.SetClientMode(r.Context(), c.ID, body.Mode), "set the client's mode") {
		return
	}
	s.log.Warn("client mode set", "id", c.ID, "mode", body.Mode, "ip", httpx.ClientIP(r.Context()))
	s.writeClient(w, r, c.ID, nil)
}

// handleRemoveSlot takes an operator's own slot away, freeing its name.
func (s *Admin) handleRemoveSlot(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadClient(w, r)
	if !ok {
		return
	}
	if s.clientError(w, s.store.RemoveSlot(r.Context(), c.ID, r.PathValue("slot")), "remove the slot") {
		return
	}
	s.writeClient(w, r, c.ID, nil)
}

// handleSetClientDisabled switches a client off or back on, from its next
// request: keys are read afresh on every one.
func (s *Admin) handleSetClientDisabled(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadClient(w, r)
	if !ok {
		return
	}
	var body struct {
		Disabled *bool `json:"disabled"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if body.Disabled == nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, `expected {"disabled": true|false}`)
		return
	}
	if s.clientError(w, s.store.SetClientDisabled(r.Context(), c.ID, *body.Disabled), "switch the client") {
		return
	}
	s.log.Warn("client switched", "id", c.ID, "name", c.Name, "disabled", *body.Disabled,
		"ip", httpx.ClientIP(r.Context()))
	s.writeClient(w, r, c.ID, nil)
}

// handleSetClientNameChats switches the gateway's naming of a client's chats,
// from its next request. A type that names its own chats has no switch, and
// the store refuses it (400).
func (s *Admin) handleSetClientNameChats(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadClient(w, r)
	if !ok {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if body.Enabled == nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, switchUsage)
		return
	}
	if s.clientError(w, s.store.SetClientNameChats(r.Context(), c.ID, *body.Enabled), "switch chat naming") {
		return
	}
	s.log.Info("client chat naming switched", "id", c.ID, "enabled", *body.Enabled)
	s.writeClient(w, r, c.ID, nil)
}

// handleSetClientFitImages switches a client's image fitting, from its next
// request.
func (s *Admin) handleSetClientFitImages(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadClient(w, r)
	if !ok {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if body.Enabled == nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, switchUsage)
		return
	}
	if s.clientError(w, s.store.SetClientFitImages(r.Context(), c.ID, *body.Enabled), "switch image fitting") {
		return
	}
	s.log.Info("client image fitting switched", "id", c.ID, "enabled", *body.Enabled)
	s.writeClient(w, r, c.ID, nil)
}

// handleSetClientAgentManage switches whether a client's own agent may change
// it through the MCP server, from its next call: the key is read afresh on
// every one. A warning, as switching a client off is: it hands a model the
// operator's choice of what the client runs.
func (s *Admin) handleSetClientAgentManage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadClient(w, r)
	if !ok {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if body.Enabled == nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, switchUsage)
		return
	}
	if s.clientError(w, s.store.SetClientAgentManage(r.Context(), c.ID, *body.Enabled), "switch agent management") {
		return
	}
	s.log.Warn("client agent management switched", "id", c.ID, "name", c.Name, "enabled", *body.Enabled,
		"ip", httpx.ClientIP(r.Context()))
	s.writeClient(w, r, c.ID, nil)
}

// handleClientFiles is a typed client's files as its page shows them: written
// by the gateway (internal/service/clientconfig) with its names and the facts
// of what each serves now, for the address the page gives (?base=), with the
// example's key where the key goes. The page swaps that for the key while it
// is offered; the install command's files are built with the key itself
// (handleKeyInstall).
func (s *Admin) handleClientFiles(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadClient(w, r)
	if !ok {
		return
	}
	base, err := clientconfig.Base(r.URL.Query().Get("base"))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, err.Error())
		return
	}
	setup := s.setupOf(r, c)
	files := clientconfig.Files(c.Type, setup, base)
	if files == nil {
		files = []clientconfig.File{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"files": files})
}

// setupOf is what a client's files are written from: its names, each with the
// facts of the model it serves now, read off the list its key is offered. A
// list that cannot be read — no account connected yet, an upstream down —
// still gives files: each name with the defaults a file gives a model it
// knows nothing of, which is a working setup to update later rather than no
// setup at all.
func (s *Admin) setupOf(r *http.Request, c store.Client) clientconfig.Setup {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	list, err := s.models.For(ctx, c.APIKey)
	if err != nil {
		s.log.Warn("could not read a client's model list; writing its files without the models' facts", "id", c.ID, "err", err)
	}
	return clientconfig.For(c, list)
}

// liveMinutes is how far back the Limits page's live chart reaches.
const liveMinutes = 60

// handleClientLimits is what a client is doing now, for its Limits page,
// polled: its requests per minute over the last hour, what it sent in its
// rate period and its budget's day, and what it has open this second.
func (s *Admin) handleClientLimits(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadClient(w, r)
	if !ok {
		return
	}
	now := time.Now()
	period := c.Period()
	// Back as far as the longer of the two windows read: a rate period can be
	// up to a week, and the budget's is a day.
	since := now.Add(-max(limits.BudgetWindow, period))
	reqs, err := s.store.KeyRequests(r.Context(), c.ID, since)
	if err != nil {
		httpx.Internal(w, s.log, "read the client's requests", err)
		return
	}
	perMinute := make([]int, liveMinutes)
	start := now.Truncate(time.Minute).Add(-(liveMinutes - 1) * time.Minute)
	var inPeriod int
	var day int64
	for _, q := range reqs {
		if q.At.After(now.Add(-limits.BudgetWindow)) {
			day += q.Tokens
		}
		if !q.At.Before(start) {
			if i := int(q.At.Sub(start) / time.Minute); i >= 0 && i < liveMinutes {
				perMinute[i]++
			}
		}
		if q.At.After(now.Add(-period)) {
			inPeriod++
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"per_minute":       perMinute,
		"since":            start.UTC().Format(time.RFC3339),
		"period_s":         int(period.Seconds()),
		"in_period":        inPeriod,
		"tokens_day":       day,
		"in_flight":        s.inflight.Open(c.ID),
		"rpm_limit":        c.RPMLimit,
		"default_rpm":      s.cfg.Limits.RequestsPerMinute,
		"token_budget":     c.TokenBudget,
		"budget_hours":     int(limits.BudgetWindow.Hours()),
		"history_recorded": s.cfg.Usage.Enabled(),
	})
}

// handleClientSuggest reads limits off a client's own history, over a week
// and over a month, as one-click presets.
func (s *Admin) handleClientSuggest(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadClient(w, r)
	if !ok {
		return
	}
	now := time.Now()
	reqs, err := s.store.KeyRequests(r.Context(), c.ID, now.Add(-30*24*time.Hour))
	if err != nil {
		httpx.Internal(w, s.log, "read the client's requests", err)
		return
	}
	uses := toUses(reqs)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"suggestions": []limits.Suggestion{limits.Suggest(uses, now, 7), limits.Suggest(uses, now, 30)},
	})
}

// replayDays is how far back a limit about to be saved is tried against.
const replayDays = 7

// handleClientPreview answers what limits would have refused of the client's
// last week, before the operator saves them.
func (s *Admin) handleClientPreview(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadClient(w, r)
	if !ok {
		return
	}
	var body keyLimitsBody
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	reqs, err := s.store.KeyRequests(r.Context(), c.ID, time.Now().Add(-replayDays*24*time.Hour))
	if err != nil {
		httpx.Internal(w, s.log, "read the client's requests", err)
		return
	}
	count, period := body.RPMLimit, time.Duration(body.RatePeriodS)*time.Second
	if period <= 0 {
		period = time.Minute
	}
	if count == 0 {
		count, period = s.cfg.Limits.RequestsPerMinute, time.Minute
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"days":   replayDays,
		"replay": limits.ReplayLimits(toUses(reqs), count, period, body.TokenBudget),
	})
}

func toUses(reqs []store.KeyRequest) []limits.Use {
	out := make([]limits.Use, len(reqs))
	for i, q := range reqs {
		out[i] = limits.Use{At: q.At, Tokens: q.Tokens}
	}
	return out
}

// windowDays reads the ?days= window a usage page asks for: 1, 7, 14 or 30,
// and 7 for anything else. 14 for Activity, whose tables share a picker with
// a chart of days, where a fortnight is the reading of a trend.
func windowDays(r *http.Request) int {
	switch r.URL.Query().Get("days") {
	case "1":
		return 1
	case "14":
		return 14
	case "30":
		return 30
	}
	return 7
}

// handleClientUsage is a client's traffic by model name and the model each
// served, over a window, and its estimated share of each provider's pooled
// subscription windows now.
func (s *Admin) handleClientUsage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadClient(w, r)
	if !ok {
		return
	}
	days := windowDays(r)
	rows, err := s.store.SlotUsage(r.Context(), store.SlotScope{KeyID: c.ID}, time.Now().AddDate(0, 0, -days))
	if err != nil {
		httpx.Internal(w, s.log, "read the client's usage", err)
		return
	}
	out := map[string]any{"days": days, "rows": rows}
	if s.cfg.Usage.Enabled() {
		if windows, err := s.subscriptionWindows(r.Context()); err == nil {
			out["share"] = shareOf(c.ID, windows)
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleAccountClients is which clients drew on one account over a window,
// by model name and model, and each one's estimated share of this account's
// windows now: the account's page names what spent it.
func (s *Admin) handleAccountClients(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.Account(r.Context(), id); errors.Is(err, store.ErrAccountNotFound) {
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such account")
		return
	}
	days := windowDays(r)
	rows, err := s.store.SlotUsage(r.Context(), store.SlotScope{AccountID: id}, time.Now().AddDate(0, 0, -days))
	if err != nil {
		httpx.Internal(w, s.log, "read the account's usage", err)
		return
	}
	shares := map[string]AccountShare{}
	if s.cfg.Usage.Enabled() {
		if windows, err := s.subscriptionWindows(r.Context()); err == nil {
			for _, row := range rows {
				if _, done := shares[row.KeyID]; done {
					continue
				}
				for _, a := range shareOf(row.KeyID, windows).Accounts {
					if a.AccountID == id {
						shares[row.KeyID] = a
					}
				}
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"days": days, "rows": rows, "shares": shares})
}
