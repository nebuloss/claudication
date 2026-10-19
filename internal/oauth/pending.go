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
	// account is the stored account this attempt renews, or empty for one
	// that adds whichever account the operator approves as.
	account string
	// device is the device-code attempt, for a provider that signs in that
	// way; zero for a consent-and-paste one.
	device    Device
	expiresAt time.Time
}

// Attempt is a resolved in-flight login.
type Attempt struct {
	PKCE        PKCE
	RedirectURI string
	// Account is the stored account being reconnected, empty when the attempt
	// adds a new one. Kept with the attempt rather than sent back by the
	// browser, so completing it cannot be pointed at a different account.
	Account string
	// Device is the device-code attempt; zero for a paste one.
	Device Device
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
	return p.StartFor(provider, state, pkce, redirectURI, "")
}

// StartFor registers an attempt that renews the stored account with the given
// id, and returns when it expires.
func (p *Pending) StartFor(provider, state string, pkce PKCE, redirectURI, account string) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evictLocked()

	expires := p.now().Add(p.ttl)
	p.flows[state] = &flow{
		provider:    provider,
		pkce:        pkce,
		redirectURI: redirectURI,
		account:     account,
		expiresAt:   expires,
	}
	return expires
}

// StartDevice registers a device-code attempt, renewing the stored account
// with the given id when it is not empty, and returns when it expires: the
// pending store's own lifetime or the code's, whichever is sooner, since a
// code the provider has forgotten cannot be approved.
func (p *Pending) StartDevice(provider, state string, d Device, account string) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evictLocked()

	expires := p.now().Add(p.ttl)
	if !d.ExpiresAt.IsZero() && d.ExpiresAt.Before(expires) {
		expires = d.ExpiresAt
	}
	p.flows[state] = &flow{
		provider:  provider,
		account:   account,
		device:    d,
		expiresAt: expires,
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
	return Attempt{PKCE: f.pkce, RedirectURI: f.redirectURI, Account: f.account, Device: f.device}, nil
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
