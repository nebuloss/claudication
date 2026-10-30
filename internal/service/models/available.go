package models

import (
	"context"
	"sort"
	"strings"
	"time"

	"claudication/internal/pool"
	"claudication/internal/service/clients"
	"claudication/internal/store"
)

// Availability: what a client can be offered, decided here once for every
// surface that offers models — a slot's menu on the admin UI, a custom
// client's direct-mode list, and /v1/models — so none of them advertises a
// model that would only be met as a refusal.
//
// The owner's rule (2026-10-20): a model is available to a client when
//
//  1. a connected, switched-on account of its provider lists it — the
//     accounts' own lists, never the public catalogue the model list falls
//     back on before the first sign-in, and
//  2. at least one such account has it on, and can serve now (pool.Offers,
//     the candidates Acquire picks from), and
//  3. the client is allowed it: the operator has not switched it off on the
//     client's Models page (Off, api_key_models_off in schema.sql).
//
// The third is the owner's (2026-10-22): it is what an agent working through
// the gateway's MCP server may switch its client to, so it has to hold for
// every way a model reaches the client — the admin menu, list_models,
// bind_model and add_model, and the request itself — which is why it is
// here, in the one rule, and not beside each of them.
//
// The client's API is no part of it: both APIs reach both providers, a model
// of the other one's translated (internal/api), so what a client is offered
// is the same whichever it speaks. It was a third rule while Messages reached
// Claude alone.
//
// A family is available when some account would serve it a version the
// client is allowed (family.go) — the family itself allowed too, its switch
// being its id's (claude-opus) — so it is offered while any account can
// serve it, whichever version that is, and refused only when none can.
//
// And a model is offered only while it works somewhere (the owner's,
// 2026-10-26): one failing on every account that has it
// (internal/service/availability) — gemini-2.5-pro, listed with quota and
// refused by Google for every request — is left out of every menu and list
// until a check or a client's request finds it served again. A family is,
// when every account would serve it a failing version; one that skips a
// failing version for a working one is not (pool.Best). Routing is not
// this rule: a request for it is still sent, and the upstream says. A
// binding made before something became unavailable is kept, and said: the
// operator sees what it was and why, and replaces it with something
// available; the gateway's own refusal of such a request is unchanged.

// The reasons a binding is unavailable, as the admin UI shows them beside it.
const (
	whyNotAllowed = "not allowed for this client"
	whyNotListed  = "no connected account lists it"
	whyOff        = "it is switched off on every account that lists it"
	whyNoAccount  = "no account that has it on can serve now"
	// whyFailing is a model that every account having it fails to serve
	// (internal/service/availability): left out of what clients are offered
	// until one serves it, which the next check or client request says.
	whyFailing = "it is failing on every account that has it; checked again every 15 minutes, " +
		"and offered again once one serves it"
)

// Off is what one client may not be served: the models switched off on its
// Models page, as the store keeps them (store.APIKey.ModelsOff). Kept as
// what is off, so a model released tomorrow arrives allowed. nil allows
// everything — what the admin's own list of switches is read with, so a
// model switched off can still be found and switched back on.
type Off []string

// allows reports whether the client may be served model, matched as the
// store matches a switch (store.ModelKey).
func (o Off) allows(model string) bool {
	return store.APIKey{ModelsOff: o}.Allows(model)
}

// Choice is one thing a slot can be bound to: a family, or one real model,
// with what it serves now.
type Choice struct {
	Model   string         `json:"model,omitempty"`
	Follows clients.Follow `json:"follows,omitempty"`
	// Family is a family's model id (claude-opus), what a menu names it by
	// beside the version it serves: the id is the name a client sees.
	Family string `json:"family,omitempty"`
	// Title is how a menu names it: the model's display name, or the
	// family's.
	Title string `json:"title"`
	// Serving is the real model it serves now — the model itself, or the
	// newest version of the family an account would serve it with — and
	// Provider whose accounts serve it.
	Serving  string `json:"serving"`
	Provider string `json:"provider"`
	// Versions, for a family, are the versions some account would serve it
	// with now, newest first: which one a request gets is the serving
	// account's (family.go), or the one the family is set to. One, when every
	// account has the same.
	Versions []string `json:"versions,omitempty"`
	// Limited is set when it is available but every account that would
	// serve it has reached its limit or is cooling down after a refusal, and
	// Back is when the first of them has room again (zero when no account
	// said). Requests still go upstream — an account at its limit is tried
	// rather than none — and are refused there until then, which is what a
	// client's page has to say: "available" alone read as working.
	Limited bool      `json:"limited,omitempty"`
	Back    time.Time `json:"back,omitzero"`
}

// Choices are everything a slot can be bound to now, provider by provider in
// the gateway's order: what Available offers a direct-mode client, a family
// being bound as the family rather than named by its id. So a provider
// offered by family lists each family it can serve and any model no family
// takes, and one offering every model lists each model, and no family: which
// version a family serves is the gateway's choice (Settings › Models), never
// a name's. Nothing unavailable is in it.
func (s *Catalog) Choices(ctx context.Context, off Off) []Choice {
	out := s.Available(ctx, off)
	for i := range out {
		if out[i].Follows != "" {
			out[i].Model = ""
		}
	}
	return out
}

// Available are what a custom client in direct mode can name now, each as a
// Choice whose Model is the id it asks for: in a provider offered by family,
// each family under its id (claude-opus) and any model no family takes; in
// one offering every model, each model. A version stays nameable in either —
// a request for one is served as asked — and is only not listed.
func (s *Catalog) Available(ctx context.Context, off Off) []Choice {
	out := []Choice{}
	for _, r := range s.relays {
		p := r.Wire.ID()
		models := s.availableModels(ctx, off, p)
		if !s.ByFamily(p) {
			out = append(out, models...)
			continue
		}
		for _, f := range s.families(ctx, p) {
			if c, why := s.Resolve(ctx, off, store.Binding{Follows: f.Follows}); why == "" {
				c.Model = f.Follows.ModelID()
				out = append(out, c)
			}
		}
		for _, m := range models {
			if s.familyOf(p, m.Model) == "" {
				out = append(out, m)
			}
		}
	}
	return out
}

// availableModels are a provider's real models a client can be served now.
func (s *Catalog) availableModels(ctx context.Context, off Off, p string) []Choice {
	s.refreshFamilies(ctx)
	s.rolesMu.Lock()
	models := append([]listedModel(nil), s.listed[p]...)
	s.rolesMu.Unlock()
	var out []Choice
	for _, m := range models {
		if c, why := s.Resolve(ctx, off, store.Binding{Model: m.ID}); why == "" && c.Provider == p {
			if m.Name != "" {
				c.Title = m.Name
			}
			out = append(out, c)
		}
	}
	return out
}

// Resolve is what binding b serves now, and why it cannot when it cannot: ""
// for a binding a client can be offered. The Choice is filled either way, so
// a binding that has become unavailable is still shown as what it is. A
// model naming a family (claude-opus, as a direct-mode client does) is that
// family.
func (s *Catalog) Resolve(ctx context.Context, off Off, b store.Binding) (Choice, string) {
	c := Choice{Model: b.Model, Follows: b.Follows, Title: b.Model, Serving: b.Model}
	f := b.Follows
	if b.Model != "" {
		f = clients.FollowOf(b.Model)
		if f != "" {
			c.Follows = f
		}
	}
	if f != "" {
		c.Title, c.Provider, c.Family = f.Title(), f.Provider(), f.ModelID()
		r, why := s.resolveFamily(ctx, off, f)
		if len(r.serving) > 0 {
			c.Serving, c.Versions = r.serving[0], r.serving
		} else if len(r.allowed) > 0 {
			c.Serving = r.allowed[0]
		}
		if why == "" && r.failing {
			why = whyFailing
		}
		if why == "" {
			c.Limited, c.Back = s.pool.Limited(pool.WithVersions(ctx, s.versionsFor(r.allowed)), c.Provider, "")
		}
		return c, why
	}
	c.Provider = s.ServingProvider(ctx, c.Serving)
	// The operator's decision for this client first: it holds whatever the
	// accounts do, and is the one thing on the client's own page to change.
	if !off.allows(c.Serving) {
		return c, whyNotAllowed
	}
	why := s.unavailable(ctx, c.Provider, c.Serving)
	if why == "" {
		c.Limited, c.Back = s.pool.Limited(ctx, c.Provider, c.Serving)
	}
	return c, why
}

// NewSlot is the binding a name added to a client starts on, what it serves,
// and why it cannot be made, "" when it can: one rule for the admin page's
// Add and the MCP server's add_model. None given follows Claude Opus, as
// every type's main name starts on, and is made whether or not an account
// serves it yet — the page then shows it unavailable, as a new client's own
// names are until an account is connected. One given has to be available
// now, as a rebinding has to. A model naming a family is bound to the family.
func (s *Catalog) NewSlot(ctx context.Context, off Off, b store.Binding) (store.Binding, Choice, string) {
	b = Normal(b)
	if b.Model == "" && b.Follows == "" {
		b.Follows = clients.FollowOpus
		c, _ := s.Resolve(ctx, off, b)
		return b, c, ""
	}
	if b.Follows != "" && !b.Follows.Valid() {
		// The store says what is wrong with it.
		return b, Choice{Follows: b.Follows, Title: string(b.Follows)}, ""
	}
	c, why := s.Bindable(ctx, off, b)
	return b, c, why
}

// Bindable is Resolve for a binding being made now: it has to be one of the
// client's Choices, so a family where its provider is offered by family and
// a model where every model is — the page's one menu, the agent's
// list_models, and the API alike. A binding made before the mode changed is
// kept, and goes on serving (Resolve); it is only not made anew.
func (s *Catalog) Bindable(ctx context.Context, off Off, b store.Binding) (Choice, string) {
	c, why := s.Resolve(ctx, off, b)
	if why != "" {
		return c, why
	}
	title := clients.ProviderTitle(c.Provider)
	switch {
	case c.Follows != "" && !s.ByFamily(c.Provider):
		return c, "Settings › Models offers every " + title + " model, so a name is bound to one of them"
	case c.Follows == "" && s.ByFamily(c.Provider):
		if f := s.familyOf(c.Provider, c.Serving); f != "" {
			return c, "Settings › Models offers " + title + " models by family, so a name is bound to " + f.Title() +
				", and which version it serves is set there"
		}
	}
	return c, ""
}

// Normal is a binding as it is stored: trimmed, and a model naming a family
// (claude-opus) bound to the family, so a slot follows it rather than keeping
// an id that only a direct-mode request would read.
func Normal(b store.Binding) store.Binding {
	b.Model = strings.TrimSpace(b.Model)
	if f := clients.FollowOf(b.Model); f != "" && b.Follows == "" {
		return store.Binding{Follows: f}
	}
	return b
}

// failingEverywhere reports whether every account that would serve model —
// switched on, listing it and having it on — is failing it now. False when
// none would: that is unavailable's other answers.
func (s *Catalog) failingEverywhere(ctx context.Context, provider, model string) bool {
	if s.failing == nil {
		return false
	}
	candidates, err := s.pool.Candidates(ctx, provider)
	if err != nil {
		return false
	}
	return s.failingAmong(candidates, model)
}

// FailingModel is a model failing on every account that would serve it.
type FailingModel struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Name     string `json:"name,omitempty"`
}

// FailingEverywhere are the models some account lists that every account
// that would serve them is failing now: what clients are not offered until
// one works again, and what the operator should hear about.
func (s *Catalog) FailingEverywhere(ctx context.Context) []FailingModel {
	if s.failing == nil {
		return nil
	}
	s.rolesMu.Lock()
	listed := make(map[string][]listedModel, len(s.listed))
	for p, ms := range s.listed {
		listed[p] = append([]listedModel(nil), ms...)
	}
	s.rolesMu.Unlock()
	var out []FailingModel
	for provider, models := range listed {
		candidates, err := s.pool.Candidates(ctx, provider)
		if err != nil {
			continue
		}
		for _, m := range models {
			if s.failingAmong(candidates, m.ID) {
				out = append(out, FailingModel{Provider: provider, Model: m.ID, Name: m.Name})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// failingAmong is failingEverywhere over accounts already read.
func (s *Catalog) failingAmong(candidates []store.Account, model string) bool {
	some := false
	for _, a := range candidates {
		if !a.Serves(model) || !s.Lists(a.ID, model) {
			continue
		}
		if !s.failing(a.ID, model) {
			return false
		}
		some = true
	}
	return some
}

// unavailable is why a real model cannot serve a client now, or "".
func (s *Catalog) unavailable(ctx context.Context, provider, model string) string {
	k := store.ModelKey(model)
	s.rolesMu.Lock()
	listed := false
	for _, m := range s.listed[provider] {
		if store.ModelKey(m.ID) == k {
			listed = true
			break
		}
	}
	s.rolesMu.Unlock()
	if !listed {
		return whyNotListed
	}
	if s.pool.Offers(ctx, provider, model) {
		if s.failingEverywhere(ctx, provider, model) {
			return whyFailing
		}
		return ""
	}
	// Listed, and no account can take it now: say which of the operator's
	// switches did that, from the accounts as they are this moment.
	accounts, err := s.store.ListAccounts(ctx)
	if err != nil {
		return whyNoAccount
	}
	enabled, on := false, false
	for _, a := range accounts {
		if a.Provider == provider && !a.Disabled() {
			enabled = true
			on = on || a.Serves(model)
		}
	}
	switch {
	case !enabled:
		return whyNotListed
	case !on:
		return whyOff
	}
	return whyNoAccount
}
