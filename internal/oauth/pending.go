package oauth

import (
	"errors"
	"sync"
	"time"
)

// Pending holds in-flight login attempts between "start" and "complete".
//
// These live in memory only: a verifier is single-use and short-lived, and a
// restart mid-login should invalidate the attempt rather than leave a usable
// credential fragment on disk.
type Pending struct {
	mu    sync.Mutex
	flows map[string]*flow
	ttl   time.Duration
	now   func() time.Time
}

type flow struct {
	provider string
	pkce     PKCE
	// redirectURI is remembered because the exchange must replay whichever
	// redirect the authorize request carried.
	redirectURI string
	expiresAt   time.Time
}

// Attempt is a resolved in-flight login.
type Attempt struct {
	PKCE        PKCE
	RedirectURI string
}

var (
	ErrUnknownState  = errors.New("this login attempt is unknown or has expired; start again")
	ErrStateMismatch = errors.New("state mismatch — the response does not belong to this login attempt")
)

func NewPending(ttl time.Duration) *Pending {
	return &Pending{
		flows: make(map[string]*flow),
		ttl:   ttl,
		now:   time.Now,
	}
}

// Start registers a new attempt and returns when it expires.
func (p *Pending) Start(provider, state string, pkce PKCE, redirectURI string) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evictLocked()

	expires := p.now().Add(p.ttl)
	p.flows[state] = &flow{
		provider:    provider,
		pkce:        pkce,
		redirectURI: redirectURI,
		expiresAt:   expires,
	}
	return expires
}

// Peek resolves an attempt without consuming it.
//
// Deliberately separate from Consume: the verifier is only spent once the
// exchange has actually succeeded. Consuming on lookup means any upstream
// failure — a mistyped paste, a transient 5xx, a rejected code — destroys the
// verifier too, and the operator has to restart the whole browser flow to try
// again. Holding it until success costs nothing: the authorization code is
// single-use at the provider, so a retry is only ever a retry.
func (p *Pending) Peek(provider, state string) (Attempt, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evictLocked()

	f, ok := p.flows[state]
	if !ok {
		return Attempt{}, ErrUnknownState
	}
	if f.provider != provider {
		return Attempt{}, ErrStateMismatch
	}
	return Attempt{PKCE: f.pkce, RedirectURI: f.redirectURI}, nil
}

// Consume discards an attempt once it has been redeemed.
func (p *Pending) Consume(state string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.flows, state)
}

func (p *Pending) evictLocked() {
	now := p.now()
	for k, f := range p.flows {
		if now.After(f.expiresAt) {
			delete(p.flows, k)
		}
	}
}

// Len reports the number of live attempts.
func (p *Pending) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evictLocked()
	return len(p.flows)
}
