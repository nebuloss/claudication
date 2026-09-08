package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"claudication/internal/store"
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

// budgets tracks what each key has spent inside the window.
type budgets struct {
	mu    sync.Mutex
	spent map[string]*budgetEntry
	now   func() time.Time // injectable for tests
}

type budgetEntry struct {
	tokens    int64
	refreshed time.Time
}

func newBudgets() *budgets {
	return &budgets{spent: make(map[string]*budgetEntry), now: time.Now}
}

// spentBy reports the key's spend inside the window, reading through to the
// database when the cached figure has gone stale.
func (b *budgets) spentBy(ctx context.Context, st *store.Store, keyID string) (int64, error) {
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
	tokens, err := st.KeySpend(ctx, keyID, now.Add(-BudgetWindow))
	if err != nil {
		return 0, err
	}

	b.mu.Lock()
	b.spent[keyID] = &budgetEntry{tokens: tokens, refreshed: now}
	b.mu.Unlock()
	return tokens, nil
}

// add credits a request that has just finished, so the cached figure keeps up
// with spending between refreshes rather than letting a burst through on a
// reading taken thirty seconds ago.
func (b *budgets) add(keyID string, tokens int64) {
	if tokens <= 0 {
		return
	}
	b.mu.Lock()
	if e, ok := b.spent[keyID]; ok {
		e.tokens += tokens
	}
	b.mu.Unlock()
}

// forget drops a key's cached figure, so a budget change takes effect at once
// instead of after the refresh interval.
func (b *budgets) forget(keyID string) {
	b.mu.Lock()
	delete(b.spent, keyID)
	b.mu.Unlock()
}

// sweep drops entries nothing has asked about for a while, so a gateway that
// has issued and deleted many keys does not hold them all for ever.
func (b *budgets) sweep(idle time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cutoff := b.now().Add(-idle)
	for id, e := range b.spent {
		if e.refreshed.Before(cutoff) {
			delete(b.spent, id)
		}
	}
}

// withinBudget answers whether the key may spend, and writes the refusal when
// it may not. Returns true to carry on.
//
// A key can overshoot its budget by one request, necessarily: what a request
// will cost is only known once it has been served. The budget is a ceiling on
// what has already been spent, not a reservation against what is about to be.
func (s *Server) withinBudget(w http.ResponseWriter, r *http.Request, key store.APIKey) bool {
	if key.TokenBudget <= 0 || !s.cfg.Usage.Enabled() {
		// No budget, or no usage history to measure one against — enforcing a
		// budget with retention off would refuse everything the moment the
		// figure could not be read.
		return true
	}

	spent, err := s.budgets.spentBy(r.Context(), s.store, key.ID)
	if err != nil {
		// Fail open, loudly. A database that cannot be read is a reason to
		// stop counting, not a reason to stop serving: turning a transient
		// storage fault into a total outage is the worse failure, and the
		// budget exists to bound spending rather than to guard anything.
		s.log.Error("could not read the token budget; allowing the request",
			"err", err, "api_key", key.Display(), "request_id", requestIDFrom(r.Context()))
		return true
	}
	if spent < key.TokenBudget {
		return true
	}

	// Say when it frees up rather than leaving the client to guess. The window
	// rolls, so the first relief comes when the oldest counted request ages out.
	if at, ok := s.store.OldestSpendAt(r.Context(), key.ID, s.budgets.now().Add(-BudgetWindow)); ok {
		if wait := time.Until(at.Add(BudgetWindow)); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		}
	}
	s.log.Warn("api key is over its token budget",
		"api_key", key.Display(), "api_key_name", key.Name,
		"spent", spent, "budget", key.TokenBudget,
		"request_id", requestIDFrom(r.Context()))

	// rate_limit_error because that is the type a client knows how to read, and
	// this is the same shape of problem: too much, too soon, try later.
	writeError(w, http.StatusTooManyRequests, "rate_limit_error",
		fmt.Sprintf("this API key has spent %d of its %d token budget for the last %s",
			spent, key.TokenBudget, BudgetWindow))
	return false
}

func (b *budgets) runSweeper(stop <-chan struct{}, every, idle time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			b.sweep(idle)
		}
	}
}
