package models

import (
	"context"
	"regexp"
	"strings"

	"claudication/internal/store"
)

// Routing: which upstream serves a request, decided from the model it names.
//
// The owner's rule (2026-10-19): clients ask for aliases, and the operator
// moves what an alias serves; nothing is routed by guessing from a name. So:
//
//  1. A name that is an alias resolves as it always has — the key's binding,
//     else the client's, else the gateway's, else what the built-in follows
//     (RoleTarget) — to a real model.
//  2. The real model, asked for directly or reached through an alias, is
//     served by the provider whose accounts list it. Ids are disjoint across
//     providers today, but the lists, not the shape of an id, are the
//     authority.
//  3. A name no list knows goes where the dialect sends unknown names: the
//     OpenAI dialect to its fallback alias (claude-large, as every Codex
//     request went before there was a second provider), anything else to the
//     default provider unchanged, which is what happened to it before too.
//
// Whether the caller's API can reach the provider chosen is the gateway's to
// decide (internal/httpapi/gateway), since it is the dialect that cannot.

// Route is where one request goes.
type Route struct {
	// Provider serves it.
	Provider string
	// Model is the real model to send.
	Model string
	// Alias is the alias the request named, when it named one; the answer
	// says it back (passes.ModelRenamer).
	Alias string
	// Why is set when the request cannot be served as asked — its alias
	// points nowhere, or at a model the key may not use or no account has
	// on — and says so, naming what to change.
	Why string
}

// Route decides where a request naming requested goes, for a key and a
// client. fallback is the alias the caller's dialect sends a name no list
// knows to, or empty to send it to the default provider as it is.
func (s *Catalog) Route(ctx context.Context, key store.APIKey, client, requested, fallback string) Route {
	if requested != "" && s.IsAlias(ctx, requested) {
		return s.aliasRoute(ctx, key, client, store.ModelKey(requested))
	}
	if p := s.ProviderOf(ctx, requested); p != "" {
		return Route{Provider: p, Model: requested}
	}
	if fallback != "" {
		return s.aliasRoute(ctx, key, client, store.ModelKey(fallback))
	}
	return Route{Provider: s.defaultProvider(), Model: requested}
}

func (s *Catalog) aliasRoute(ctx context.Context, key store.APIKey, client, alias string) Route {
	target, why := s.RoleTarget(ctx, key, client, alias)
	p := s.ProviderOf(ctx, target)
	if p == "" {
		p = s.defaultProvider()
	}
	return Route{Provider: p, Model: target, Alias: alias, Why: why}
}

// prefixes say which provider an id belongs to when a list cannot: always for
// the default provider, Anthropic, whose lists never decided what it serves —
// a Claude model it does not list was sent to it before there was a second
// provider, and the upstream said whether it knew it — and for another only
// while it has an account switched on none of whose lists could be read, so
// an upstream briefly unreachable does not send its models somewhere else.
// Never for one with no account: a Codex configured with an id of its own
// keeps working, on a gateway with no ChatGPT account, as it did.
var prefixes = []struct {
	provider string
	match    *regexp.Regexp
}{
	{"anthropic", regexp.MustCompile(`^claude-`)},
	{"openai", regexp.MustCompile(`^(gpt-|codex-|o[0-9])`)},
}

// ProviderOf is the provider whose accounts list model, or "" when none does.
// The id's shape decides only where a list cannot; see prefixes.
//
// A Claude id is the default provider's without reading a list at all: that
// is what it always was, and a request naming a real Claude model — nearly
// every one Claude Code sends — must not wait on, or refresh a token for, a
// round of model lists to learn it.
func (s *Catalog) ProviderOf(ctx context.Context, model string) string {
	if model == "" {
		return ""
	}
	lower := strings.ToLower(model)
	def := s.defaultProvider()
	for _, p := range prefixes {
		if p.provider == def && p.match.MatchString(lower) {
			return def
		}
	}
	s.refreshFamilies(ctx)
	k := store.ModelKey(model)
	s.rolesMu.Lock()
	defer s.rolesMu.Unlock()
	for _, r := range s.relays {
		if s.served[r.Wire.ID()][k] {
			return r.Wire.ID()
		}
	}
	for _, p := range prefixes {
		if s.connected[p.provider] && s.unread[p.provider] && p.match.MatchString(lower) {
			return p.provider
		}
	}
	return ""
}
