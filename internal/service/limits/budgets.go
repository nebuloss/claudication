// Package limits is how much a caller may do: request rates per key and per
// address, and token budgets per key.
//
// It decides and counts; it does not answer. Refusing a request — the status,
// the Retry-After, the error envelope a client can read — is the HTTP layer's,
// which asks here first.
package limits

import (
	"context"
	"sync"
	"time"
)

// BudgetWindow is the period a key's token budget covers.
//
// Rolling rather than calendar-aligned, so the budget frees up continuously
// instead of everything becoming possible again at midnight — the cliff is what
// makes a fixed period worth gaming. A day is the horizon that matters for a
// gateway in front of a subscription: long enough that ordinary work never
// notices, short enough that one runaway agent cannot spend a week's worth
// before anybody looks.
const BudgetWindow = 24 * time.Hour

// budgetFresh is how stale a cached figure may be before it is re-read.
//
// The check sits on the request path, so it cannot be a query every time. Thirty
// seconds of drift is bounded in the only direction that matters: spend is
// credited to the cache as it happens, so the figure is late in noticing tokens
// that ageing out has *freed*, never late in noticing tokens spent.
const budgetFresh = 30 * time.Second

// Spender reads what a key has spent since a moment. The store is one.
type Spender interface {
	KeySpend(ctx context.Context, keyID string, since time.Time) (int64, error)
}

// Budgets tracks what each key has spent inside the window.
type Budgets struct {
	src   Spender
	mu    sync.Mutex
	spent map[string]*budgetEntry
	now   func() time.Time // injectable for tests
}

type budgetEntry struct {
	tokens    int64
	refreshed time.Time
}

// NewBudgets returns a tracker reading spend from src.
func NewBudgets(src Spender) *Budgets {
	return &Budgets{src: src, spent: make(map[string]*budgetEntry), now: time.Now}
}

// Now is the tracker's clock, for working out when a window ends.
func (b *Budgets) Now() time.Time { return b.now() }

// SpentBy reports the key's spend inside the window, reading through to the
// source when the cached figure has gone stale.
func (b *Budgets) SpentBy(ctx context.Context, keyID string) (int64, error) {
	b.mu.Lock()
	now := b.now()
	if e, ok := b.spent[keyID]; ok && now.Sub(e.refreshed) < budgetFresh {
		tokens := e.tokens
		b.mu.Unlock()
		return tokens, nil
	}
	b.mu.Unlock()

	// Deliberately outside the lock: two requests arriving together may both
	// read, which costs one extra query and cannot produce a wrong answer,
	// where holding the mutex across a query would serialise every request for
	// every key behind one database round trip.
	tokens, err := b.src.KeySpend(ctx, keyID, now.Add(-BudgetWindow))
	if err != nil {
		return 0, err
	}

	b.mu.Lock()
	b.spent[keyID] = &budgetEntry{tokens: tokens, refreshed: now}
	b.mu.Unlock()
	return tokens, nil
}

// Add credits a request that has just finished, so the cached figure keeps up
// with spending between refreshes rather than letting a burst through on a
// reading taken thirty seconds ago.
func (b *Budgets) Add(keyID string, tokens int64) {
	if tokens <= 0 {
		return
	}
	b.mu.Lock()
	if e, ok := b.spent[keyID]; ok {
		e.tokens += tokens
	}
	b.mu.Unlock()
}

// Forget drops a key's cached figure, so a budget change takes effect at once
// instead of after the refresh interval.
func (b *Budgets) Forget(keyID string) {
	b.mu.Lock()
	delete(b.spent, keyID)
	b.mu.Unlock()
}

// Sweep drops entries nothing has asked about for a while, so a gateway that
// has issued and deleted many keys does not hold them all for ever.
func (b *Budgets) Sweep(idle time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cutoff := b.now().Add(-idle)
	for id, e := range b.spent {
		if e.refreshed.Before(cutoff) {
			delete(b.spent, id)
		}
	}
}

// RunSweeper sweeps every interval until stop closes.
func (b *Budgets) RunSweeper(stop <-chan struct{}, every, idle time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			b.Sweep(idle)
		}
	}
}
