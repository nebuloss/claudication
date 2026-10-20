package models

import (
	"context"
	"encoding/json"

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
//     the candidates Acquire picks from).
//
// The client's API is no part of it: both APIs reach both providers, a model
// of the other one's translated (internal/api), so what a client is offered
// is the same whichever it speaks. It was a third rule while Messages reached
// Claude alone.
//
// A target a slot follows is available when what it points at now is. A
// binding made before something became unavailable is kept, and said: the
// operator sees what it was and why, and replaces it with something
// available; the gateway's own refusal of such a request is unchanged.

// The reasons a binding is unavailable, as the admin UI shows them beside it.
const (
	whyNotListed = "no connected account lists it"
	whyOff       = "it is switched off on every account that lists it"
	whyNoAccount = "no account that has it on can serve now"
)

// Choice is one thing a slot can be bound to: a real model, or a target to
// follow, with what it serves now.
type Choice struct {
	Model   string         `json:"model,omitempty"`
	Follows clients.Follow `json:"follows,omitempty"`
	// Title is how a menu names it: the model's display name, or the
	// target's.
	Title string `json:"title"`
	// Serving is the real model it serves now — the model itself, or what
	// the target resolves to — and Provider whose accounts serve it.
	Serving  string `json:"serving"`
	Provider string `json:"provider"`
}

// Choices are everything a slot can be bound to now: each target that
// resolves to an available model, then each available model, provider by
// provider in the gateway's order. Nothing unavailable is in it.
func (s *Catalog) Choices(ctx context.Context) []Choice {
	s.refreshFamilies(ctx)
	out := []Choice{}
	var targets []clients.Follow
	for _, f := range s.Families(ctx) {
		targets = append(targets, clients.ClaudeFollow(f))
	}
	targets = append(targets, clients.FollowFirst, clients.FollowLight)
	for _, f := range targets {
		if c, why := s.Resolve(ctx, store.Binding{Follows: f}); why == "" {
			out = append(out, c)
		}
	}
	return append(out, s.Available(ctx)...)
}

// Available are the real models a client can be served now, each as a
// Choice: what a custom client in direct mode may name.
func (s *Catalog) Available(ctx context.Context) []Choice {
	s.refreshFamilies(ctx)
	out := []Choice{}
	for _, r := range s.relays {
		p := r.Wire.ID()
		s.rolesMu.Lock()
		models := append([]listedModel(nil), s.listed[p]...)
		s.rolesMu.Unlock()
		for _, m := range models {
			if c, why := s.Resolve(ctx, store.Binding{Model: m.ID}); why == "" && c.Provider == p {
				if m.Name != "" {
					c.Title = m.Name
				}
				out = append(out, c)
			}
		}
	}
	return out
}

// Resolve is what binding b serves now, and why it cannot when it cannot: ""
// for a binding a client can be offered. The Choice is filled either way, so
// a binding that has become unavailable is still shown as what it is.
func (s *Catalog) Resolve(ctx context.Context, b store.Binding) (Choice, string) {
	c := Choice{Model: b.Model, Follows: b.Follows, Title: b.Model, Serving: b.Model}
	if b.Model == "" {
		c.Title = b.Follows.Title()
		target, why := s.FollowTarget(ctx, b.Follows)
		if why != "" {
			c.Provider = b.Follows.Provider()
			return c, why
		}
		c.Serving = target
	}
	c.Provider = s.ServingProvider(ctx, c.Serving)
	return c, s.unavailable(ctx, c.Provider, c.Serving)
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

// onlyAvailable is a model list less what a client cannot be served now. A
// list it cannot read is returned with nothing in it: what it would have
// offered could not be checked.
func (s *Catalog) onlyAvailable(ctx context.Context, body []byte) []byte {
	keep := map[string]bool{}
	var l struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &l)
	for _, e := range l.Data {
		if _, why := s.Resolve(ctx, store.Binding{Model: e.ID}); why == "" {

			keep[e.ID] = true
		}
	}
	return onlyModels(body, keep)
}
