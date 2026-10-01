// Package accounts keeps every connected account's subscription usage
// current, so the pool routes on fresh figures and the accounts screen shows
// them.
//
// It polls rather than reading the figures off relayed responses, which is
// the whole point: an account that has not served anything still has a
// subscription, and an operator who has just connected one wants to see where
// it stands before sending it any traffic.
package accounts

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"claudication/internal/store"
)

// How often every account's usage is refreshed.
//
// Two rates, because the two situations want opposite things. With nobody
// watching, the figures only need to be honest by the time someone looks, and
// the polite thing is to leave the upstream alone. With the accounts screen
// open, the figures ARE the screen: an operator who clicks Refresh and watches
// the percentage move is asking a question the slow rate cannot answer.
//
// Both rates are usage.poll-idle and usage.poll-watched in config: the fast one
// is traffic on the operator's own subscriptions, and where the usage endpoint
// starts refusing has not been measured.
const (
	// How long after an admin request the UI counts as still open, at the
	// least. WatchWindow stretches it to a few fast intervals, so a browser
	// tab refreshing at that rate keeps itself in the watched state without a
	// gap however slow the operator configured it.
	minWatchWindow = 2 * time.Minute

	// The poller wakes this often and decides whether it is due. Sleeping in
	// short steps is what lets the rate change take effect immediately when
	// someone opens the UI, rather than after the current long sleep ends.
	// No finer than config.MinUsagePoll allows either rate to be.
	Tick = 5 * time.Second
)

// Store is where accounts and their quotas live. The database is one.
type Store interface {
	ListAccounts(ctx context.Context) ([]store.Account, error)
	SetAccountQuota(ctx context.Context, id string, q store.AccountQuota) error
}

// TokenSource hands out a usable access token for an account. The pool is
// one: it refreshes a token that is about to expire.
type TokenSource interface {
	AccessToken(ctx context.Context, id string) (string, error)
}

// FetchFunc reads one account's usage from its provider, as the quota the
// store keeps.
type FetchFunc func(ctx context.Context, token string) (store.AccountQuota, error)

// Poller refreshes account usage at a rate that follows whether anyone is
// looking.
type Poller struct {
	Store   Store
	Tokens  TokenSource
	Fetch   FetchFunc
	Idle    time.Duration
	Watched time.Duration
	Log     *slog.Logger

	seen atomic.Int64 // unix nanos of the last admin request
	now  func() time.Time
}

func (p *Poller) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

func (p *Poller) log() *slog.Logger {
	if p.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return p.Log
}

// NoteActivity records that the admin UI asked for something. Called from the
// admin middleware, so it covers every panel without each one opting in.
func (p *Poller) NoteActivity() { p.seen.Store(p.clock().UnixNano()) }

// WatchWindow is how long after the last admin request the UI counts as open.
func (p *Poller) WatchWindow() time.Duration { return max(minWatchWindow, 3*p.Watched) }

// Watching reports whether the admin UI has been heard from recently.
func (p *Poller) Watching() bool {
	last := p.seen.Load()
	return last != 0 && p.clock().Sub(time.Unix(0, last)) < p.WatchWindow()
}

// Interval is the rate the poller should currently run at.
func (p *Poller) Interval() time.Duration {
	if p.Watching() {
		return p.Watched
	}
	return p.Idle
}

// Refresh asks the provider what one account has spent and stores it.
func (p *Poller) Refresh(ctx context.Context, id string) error {
	token, err := p.Tokens.AccessToken(ctx, id)
	if err != nil {
		return err
	}
	q, err := p.Fetch(ctx, token)
	if err != nil {
		return err
	}
	return p.Store.SetAccountQuota(ctx, id, q)
}

// PollAll refreshes every account that is not paused. A failure on one is not
// an error worth raising — the account may simply be unreachable this minute,
// and the UI shows the last figure with the time it was taken.
func (p *Poller) PollAll(ctx context.Context) {
	accounts, err := p.Store.ListAccounts(ctx)
	if err != nil {
		p.log().Warn("could not list accounts for the usage poll", "err", err)
		return
	}
	for _, a := range accounts {
		if a.Disabled() {
			continue
		}
		if err := p.Refresh(ctx, a.ID); err != nil {
			p.log().Debug("usage poll failed", "account", a.Email, "err", err)
		}
	}
}

// Run polls once at once, then whenever the current interval has passed,
// until ctx ends or stop closes.
func (p *Poller) Run(ctx context.Context, stop <-chan struct{}, tick time.Duration) {
	p.PollAll(ctx)
	last := p.clock()

	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			if p.clock().Sub(last) < p.Interval() {
				continue
			}
			p.PollAll(ctx)
			// Measured from the end of the round, so a slow upstream stretches
			// the gap instead of queueing polls back to back.
			last = p.clock()
		}
	}
}
