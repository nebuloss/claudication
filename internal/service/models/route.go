package models

import (
	"context"
	"regexp"
	"strings"

	"claudication/internal/store"
)

// Routing: which upstream serves a request, decided from the model it names
// and the client whose key it carries.
//
// The owner's rule (2026-10-20): a model name means something only for the
// client that has it, and nothing is routed by guessing from a name. So:
//
//  1. A client that names its own slots — every typed client, and a custom
//     one in binding mode — asks for one of them, and it resolves through
//     that client's own table: the model the slot is bound to, or what the
//     target it follows points at now. Any other name is refused, listing
//     the client's names: each type's slots are every model field its
//     configuration has (internal/service/clients), so moving such a client
//     is always one slot's change and a real id typed into its picker
//     cannot route around it.
//  2. A custom client in direct mode names real models, each served by the
//     provider whose accounts list it.
//     Ids are disjoint across providers today, but the lists, not the shape
//     of an id, are the authority; a name no list knows goes to the default
//     provider unchanged, which says whether it knows it.
//
// Whether the caller's API can reach the provider chosen is the gateway's to
// decide (internal/httpapi/gateway), since it is the dialect that cannot.

// Route is where one request goes.
type Route struct {
	// Provider serves it.
	Provider string
	// Model is the real model to send.
	Model string
	// Slot is the client's model name the request named, when it named one;
	// the answer says it back (passes.ModelRenamer), and the request log
	// records it.
	Slot string
	// Why is set when the request cannot be served as asked — its name is
	// none of the client's, or its slot points at a model no account has on
	// — and says so, naming what to change.
	Why string
}

// Route decides where a request naming requested goes, for a key.
func (s *Catalog) Route(ctx context.Context, key store.APIKey, requested string) Route {
	if key.NamesSlots() {
		sl, ok, err := s.store.ClientSlotNamed(ctx, key.ID, requested)
		switch {
		case err != nil:
			return Route{Provider: s.defaultProvider(), Model: requested, Why: "could not read the client's model names"}
		case !ok:
			return Route{Provider: s.defaultProvider(), Model: requested, Why: s.ownNamesOnly(ctx, key, requested)}
		}
		target, why := s.SlotTarget(ctx, key.ModelsOff, sl)
		return Route{Provider: s.ServingProvider(ctx, target), Model: target, Slot: sl.Name, Why: why}
	}
	if p := s.ProviderOf(ctx, requested); p != "" {
		return Route{Provider: p, Model: requested}
	}
	return Route{Provider: s.defaultProvider(), Model: requested}
}

// ownNamesOnly is the refusal of a request for a name that is not one of its
// client's.
func (s *Catalog) ownNamesOnly(ctx context.Context, key store.APIKey, requested string) string {
	what := "a request naming no model"
	if requested != "" {
		what = requested
	}
	slots, err := s.store.ClientSlots(ctx, key.ID)
	if err != nil || len(slots) == 0 {
		return "this client has no model names yet, and " + what + " is not one; an administrator adds " +
			"them on the client's Models page"
	}
	names := make([]string, 0, len(slots))
	for _, sl := range slots {
		names = append(names, sl.Name)
	}
	return "this client asks for its own model names (" + strings.Join(names, ", ") + "), and " +
		what + " is not one; an administrator changes what each serves on the client's Models page"
}

// ServingProvider is the provider a resolved model is sent to: the one whose
// accounts list it, else the default provider, which is where a bound model
// no list names was sent before there was a second provider. One answer for
// where the request goes and for whether an account there can take it
// (SlotTarget), so the two cannot disagree.
func (s *Catalog) ServingProvider(ctx context.Context, model string) string {
	if p := s.ProviderOf(ctx, model); p != "" {
		return p
	}
	return s.defaultProvider()
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
