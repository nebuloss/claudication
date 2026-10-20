package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/service/clients"
	"claudication/internal/service/install"
	"claudication/internal/service/limits"
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
	Type     string          `json:"type"`
	Dialect  clients.Dialect `json:"dialect"`
	Mode     clients.Mode    `json:"mode"`
	Disabled bool            `json:"disabled"`
	// NameChats is its "Name chats" switch.
	NameChats bool `json:"name_chats"`
	// DisabledAt is when it was switched off, when it is.
	DisabledAt string `json:"disabled_at,omitempty"`
	// ConfiguredAt is when it was first set up with its key; from then on
	// the key and the install command are not offered (refuseConfigured).
	ConfiguredAt string `json:"configured_at,omitempty"`
	// Install is its install link's state, polled while its page is open.
	Install install.State `json:"install"`
	Slots   []SlotJSON    `json:"slots"`
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
	// Serving is the real model a request for Name gets now, Provider whose
	// accounts serve it, and Problem why it cannot be served, when it cannot.
	Serving  string `json:"serving"`
	Provider string `json:"provider,omitempty"`
	Problem  string `json:"problem,omitempty"`
	// Unreachable says the client's dialect cannot reach Provider: a
	// Messages client bound to a GPT model, which the gateway refuses. The
	// slot is shown with a warning rather than refused here.
	Unreachable bool `json:"unreachable,omitempty"`
}

// clientJSON is a client as the admin API reports it, its slots resolved.
func (s *Admin) clientJSON(ctx context.Context, c store.Client, key KeyJSON) ClientJSON {
	out := ClientJSON{KeyJSON: key, Type: c.Type, Dialect: c.Dialect, Mode: c.Mode, Disabled: c.DisabledAt != nil,
		NameChats: c.NameChats,
		Slots:     make([]SlotJSON, 0, len(c.Slots))}
	if c.DisabledAt != nil {
		out.DisabledAt = c.DisabledAt.UTC().Format(time.RFC3339)
	}
	if c.ConfiguredAt != nil {
		out.ConfiguredAt = c.ConfiguredAt.UTC().Format(time.RFC3339)
	}
	out.Install = s.install.StateOf(c.ID)
	t := c.Spec()
	for _, sl := range c.Slots {
		spec, _ := t.SlotFor(sl.Slot)
		if _, typed := c.Spec().Slot(sl.Slot); !typed {
			// An operator's own slot: its id is made up, its name is what
			// the operator knows it by.
			spec.Label = sl.Name
		}
		j := SlotJSON{Slot: spec, Name: sl.Name, Model: sl.Model, Bound: sl.Follows}
		j.Serving, j.Problem = s.models.SlotTarget(ctx, sl)
		if j.Serving != "" {
			j.Provider = s.models.ServingProvider(ctx, j.Serving)
			j.Unreachable = s.reaches != nil && !s.reaches(c.Dialect, j.Provider)
		}
		out.Slots = append(out.Slots, j)
	}
	return out
}

// FollowJSON is one moving target a slot can follow, and what it points at
// now.
type FollowJSON struct {
	ID       clients.Follow `json:"id"`
	Title    string         `json:"title"`
	Provider string         `json:"provider"`
	Serving  string         `json:"serving,omitempty"`
	Problem  string         `json:"problem,omitempty"`
}

// follows are the targets a slot can follow now: the newest of each Claude
// family the accounts list, and the ChatGPT backend's two.
func (s *Admin) follows(ctx context.Context) []FollowJSON {
	var ids []clients.Follow
	for _, f := range s.models.Families(ctx) {
		ids = append(ids, clients.ClaudeFollow(f))
	}
	ids = append(ids, clients.FollowFirst, clients.FollowLight)
	out := make([]FollowJSON, 0, len(ids))
	for _, f := range ids {
		j := FollowJSON{ID: f, Title: f.Title(), Provider: f.Provider()}
		j.Serving, j.Problem = s.models.FollowTarget(ctx, f)
		out = append(out, j)
	}
	return out
}

// handleClientTypes is the table of client types — each type's slots, the
// APIs it can be pointed at and why its default is the default — and the
// targets a slot can follow. The UI builds the add-a-client picker, every
// slot's label and the binding menus from it, so the two cannot disagree.
func (s *Admin) handleClientTypes(w http.ResponseWriter, r *http.Request) {
	type dialectJSON struct {
		ID    clients.Dialect `json:"id"`
		Title string          `json:"title"`
		Route string          `json:"route"`
	}
	dialects := []dialectJSON{}
	for _, d := range []clients.Dialect{clients.Messages, clients.Responses} {
		dialects = append(dialects, dialectJSON{ID: d, Title: d.Title(), Route: d.Route()})
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"types": clients.All, "dialects": dialects, "follows": s.follows(ctx),
	})
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
	out := map[string]any{"client": s.clientJSON(ctx, c, s.keyStats(ctx, c.ID).of(s, c.APIKey))}
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

// createClient makes a client of a type, on a dialect ("" for the type's
// default), with the type's slots under its names, each following its
// default target. The type and the dialect are the client's for good.
func (s *Admin) createClient(ctx context.Context, name string, t clients.Type, d clients.Dialect,
	lim store.KeyLimits) (store.Client, string, error) {
	d, initial := t.Initial(d)
	slots := make([]store.NewSlot, 0, len(initial))
	for _, a := range initial {
		slots = append(slots, store.NewSlot{Slot: a.Slot, Name: a.Name, Binding: store.Binding{Follows: a.Follows}})
	}
	return s.store.CreateClient(ctx, name, t.ID, d, lim, slots)
}

// handleCreateClient makes a client and its key, and answers the key's
// plaintext once, as creating a key does.
func (s *Admin) handleCreateClient(w http.ResponseWriter, r *http.Request) {
	var body struct {
		keyLimitsBody
		Type    string          `json:"type"`
		Dialect clients.Dialect `json:"dialect"`
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
	case body.Dialect != "" && !t.Speaks(body.Dialect):
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest,
			t.Label+" cannot be pointed at the "+body.Dialect.Title()+" API")
		return
	}
	lim := store.KeyLimits{RPMLimit: body.RPMLimit, RatePeriod: time.Duration(body.RatePeriodS) * time.Second,
		TokenBudget: body.TokenBudget}
	c, plaintext, err := s.createClient(r.Context(), name, t, body.Dialect, lim)
	if s.clientError(w, err, "create the client") {
		return
	}
	s.log.Info("client created", "id", c.ID, "name", c.Name, "type", c.Type, "dialect", c.Dialect,
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
	b := store.Binding{Model: body.Model, Follows: body.Follows}
	if s.clientError(w, s.store.SetSlot(r.Context(), c.ID, slot, body.Name, b), "set the slot") {
		return
	}
	s.log.Info("client slot set", "id", c.ID, "slot", slot, "name", body.Name, "model", body.Model,
		"follows", body.Follows, "ip", httpx.ClientIP(r.Context()))
	s.writeClient(w, r, c.ID, nil)
}

// handleAddSlot gives a custom client a model name of its own, following the
// newest Claude Opus unless a binding is given.
func (s *Admin) handleAddSlot(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadClient(w, r)
	if !ok {
		return
	}
	var body slotBody
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	b := store.Binding{Model: body.Model, Follows: body.Follows}
	if b.Model == "" && b.Follows == "" {
		b.Follows = clients.FollowOpus
	}
	if s.clientError(w, s.store.AddSlot(r.Context(), c.ID, strings.TrimSpace(body.Name), b), "add the slot") {
		return
	}
	s.writeClient(w, r, c.ID, nil)
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

// handleRemoveSlot takes a custom client's slot away, freeing its name.
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

// handleSetClientNameChats switches the naming of a client's chats, from its
// next request.
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

// windowDays reads the ?days= window a usage page asks for: 1, 7 or 30, and
// 7 for anything else.
func windowDays(r *http.Request) int {
	switch r.URL.Query().Get("days") {
	case "1":
		return 1
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

// handleKeyOfferedModels is the model list one key is offered — /v1/models
// as that key sees it, its client's slots and nothing else for a client that
// names them — for writing its client's configs with each slot's facts.
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
	body, err := s.models.For(ctx, key)
	if err != nil {
		s.log.Warn("could not read a key's model list", "err", err)
		httpx.WriteError(w, http.StatusBadGateway, httpx.KindUpstream, "could not reach the upstream model list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
