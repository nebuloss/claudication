package pool

import (
	"context"
	"time"

	"claudication/internal/store"
)

// Conversation affinity: for a provider that wants it, a conversation keeps
// being served by the account that served it last.
//
// ChatGPT is the one that does. Codex sends store:false with
// include:["reasoning.encrypted_content"], so every turn carries the model's
// earlier reasoning back as an encrypted blob for the backend to decrypt.
// Replaying such a blob on the account that made it was measured to work
// (scripts/probe-chatgpt.py, 2026-10-19: one Plus account, the reasoning item
// of turn one sent back in turn two, 200 and the right answer). Replaying it
// on a different account was not measured — there was one account to
// measure with — and if the blob is bound to the account that made it, a
// conversation that fails over mid-way is refused for the rest of its life.
// Keeping it where it was costs nothing while that account is healthy, and
// the prompt cache, which is per account, agrees.
//
// A preference, never a pin: an account that is cooling down or out of room
// is passed over exactly as it would be without one, because a conversation
// that might be refused on another account beats one that certainly waits.
// Anthropic gets none: priority order already keeps its conversations on the
// first account, and nothing it sends back is bound to one.

// PreferLastAccount makes Acquire prefer, for one provider, the account that
// last served the same conversation.
func (p *Pool) PreferLastAccount(provider string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.affinity.providers == nil {
		p.affinity.providers = map[string]bool{}
	}
	p.affinity.providers[provider] = true
}

// conversationKey is the context key a request's conversation travels under.
type conversationKey struct{}

// WithConversation names the conversation a request belongs to, for Acquire
// to keep it on the account that served it last. Empty names none.
//
// On the context rather than an argument because the relay asks the pool for
// an account and does not know, or need to know, which chat it is relaying.
func WithConversation(ctx context.Context, conversation string) context.Context {
	if conversation == "" {
		return ctx
	}
	return context.WithValue(ctx, conversationKey{}, conversation)
}

func conversationOf(ctx context.Context) string {
	c, _ := ctx.Value(conversationKey{}).(string)
	return c
}

// affinityTTL is how long a conversation is remembered after its last turn.
// Codex sessions pause for as long as a person is reading; an hour of quiet
// is a conversation that has ended, or one whose cache has gone cold anyway.
const affinityTTL = time.Hour

// maxAffinity bounds how many conversations are remembered. Each is a few
// dozen bytes; the bound is against a client inventing a new id per request.
const maxAffinity = 10000

type affine struct {
	account string
	at      time.Time
}

type affinity struct {
	// providers are the ones that keep it; nil is none.
	providers map[string]bool
	last      map[string]affine
}

func affinityKey(provider, conversation string) string {
	return provider + "\x00" + conversation
}

// preferred is the account that last served conversation, when the provider
// keeps affinity and that account can serve now — neither cooling nor out of
// room, the same tests pick's first pass applies.
func (p *Pool) preferred(provider, conversation string, candidates []store.Account) (store.Account, bool) {
	if conversation == "" {
		return store.Account{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.affinity.providers[provider] {
		return store.Account{}, false
	}
	last, ok := p.affinity.last[affinityKey(provider, conversation)]
	now := p.now()
	if !ok || now.Sub(last.at) > affinityTTL {
		return store.Account{}, false
	}
	for _, a := range candidates {
		if a.ID != last.account {
			continue
		}
		if p.stateOf(a.ID).cooldownUntil.After(now) || outOfRoom(a) {
			return store.Account{}, false
		}
		return a, true
	}
	return store.Account{}, false
}

// remember notes which account a conversation was just leased.
func (p *Pool) remember(provider, conversation, account string) {
	if conversation == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.affinity.providers[provider] {
		return
	}
	now := p.now()
	if len(p.affinity.last) >= maxAffinity {
		for k, v := range p.affinity.last {
			if now.Sub(v.at) > affinityTTL {
				delete(p.affinity.last, k)
			}
		}
		// Still full of live conversations: forget an arbitrary one rather
		// than grow. Losing a preference costs one cache miss, at most.
		for k := range p.affinity.last {
			if len(p.affinity.last) < maxAffinity {
				break
			}
			delete(p.affinity.last, k)
		}
	}
	p.affinity.last[affinityKey(provider, conversation)] = affine{account: account, at: now}
}
