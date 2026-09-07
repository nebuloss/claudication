// Package pool picks which upstream account serves a request, and keeps
// unhealthy ones out of rotation.
package pool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/nebuloss/claudication/internal/oauth"
	"github.com/nebuloss/claudication/internal/secret"
	"github.com/nebuloss/claudication/internal/store"
)

// FailureKind classifies why an attempt failed, which decides how long the
// account sits out.
type FailureKind string

const (
	FailureRateLimit FailureKind = "rate_limit"
	FailureAuth      FailureKind = "auth"
	FailureForbidden FailureKind = "forbidden"
	FailureServer    FailureKind = "server"
	FailureNetwork   FailureKind = "network"
)

var backoff = map[FailureKind]struct{ base, max time.Duration }{
	FailureRateLimit: {time.Minute, 15 * time.Minute},
	FailureAuth:      {10 * time.Minute, time.Hour},
	FailureForbidden: {10 * time.Minute, time.Hour},
	FailureServer:    {5 * time.Second, 5 * time.Minute},
	FailureNetwork:   {5 * time.Second, 5 * time.Minute},
}

// ClassifyStatus maps an upstream status onto a failure kind.
//
// 529 is Anthropic's documented "overloaded" code and belongs with the other
// transient server failures. auth2api omits it from its retry set, so an
// overload cools the account down and then breaks out of the loop without ever
// trying the healthy accounts sitting next to it.
func ClassifyStatus(status int) (FailureKind, bool) {
	switch {
	case status == http.StatusTooManyRequests:
		return FailureRateLimit, true
	case status == http.StatusUnauthorized:
		return FailureAuth, true
	case status == http.StatusForbidden:
		return FailureForbidden, true
	case status == 529 || status >= 500:
		return FailureServer, true
	}
	// Other 4xx are the client's request being wrong, not the account being
	// unhealthy. Do not cool down, do not retry — surface it.
	return "", false
}

var (
	ErrNoAccounts   = errors.New("no accounts are configured for this provider")
	ErrAllCoolingUp = errors.New("every account is in cooldown")
)

// Lease is one account, ready to serve a request.
type Lease struct {
	Account     store.Account
	AccessToken string
}

type health struct {
	cooldownUntil time.Time
	failures      int
	// refreshing is held for the duration of a token refresh so concurrent
	// requests wait rather than each burning a refresh. Rotating refresh
	// tokens make a double refresh actively harmful.
	refreshing chan struct{}
}

type Pool struct {
	store  *store.Store
	sealer *secret.Sealer
	client *http.Client
	log    *slog.Logger

	mu     sync.Mutex
	states map[string]*health
	now    func() time.Time
}

func New(st *store.Store, sealer *secret.Sealer, client *http.Client, log *slog.Logger) *Pool {
	return &Pool{
		store:  st,
		sealer: sealer,
		client: client,
		log:    log,
		states: make(map[string]*health),
		now:    time.Now,
	}
}

func (p *Pool) stateOf(id string) *health {
	h, ok := p.states[id]
	if !ok {
		h = &health{}
		p.states[id] = h
	}
	return h
}

// Available reports how many accounts could serve a request right now.
func (p *Pool) Available(ctx context.Context, provider string) (int, error) {
	accounts, err := p.store.ListAccounts(ctx)
	if err != nil {
		return 0, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	n := 0
	for _, a := range accounts {
		if a.Provider != provider || a.Disabled() {
			continue
		}
		if p.stateOf(a.ID).cooldownUntil.After(now) {
			continue
		}
		n++
	}
	return n, nil
}

// Acquire picks an account, refreshing its token first if it has expired.
//
// exclude lets a retry skip accounts that already failed for this request.
func (p *Pool) Acquire(ctx context.Context, provider string, exclude map[string]bool) (Lease, error) {
	accounts, err := p.store.ListAccounts(ctx)
	if err != nil {
		return Lease{}, err
	}

	candidates := make([]store.Account, 0, len(accounts))
	for _, a := range accounts {
		if a.Provider == provider && !a.Disabled() && !exclude[a.ID] {
			candidates = append(candidates, a)
		}
	}
	if len(candidates) == 0 {
		if exclude != nil && len(exclude) > 0 {
			return Lease{}, ErrAllCoolingUp
		}
		return Lease{}, ErrNoAccounts
	}

	chosen, ok := p.pick(candidates)
	if !ok {
		return Lease{}, ErrAllCoolingUp
	}

	// Refresh slightly ahead of expiry so a request never rides a token that
	// dies mid-flight.
	if p.now().Add(2 * time.Minute).After(chosen.ExpiresAt) {
		if err := p.Refresh(ctx, chosen.ID); err != nil {
			p.ReportFailure(chosen.ID, FailureAuth, err.Error())
			return Lease{}, fmt.Errorf("refresh %s: %w", chosen.Email, err)
		}
		if chosen, err = p.store.Account(ctx, chosen.ID); err != nil {
			return Lease{}, err
		}
	}

	tokens, err := p.store.AccountTokens(ctx, p.sealer, chosen.ID)
	if err != nil {
		return Lease{}, err
	}
	return Lease{Account: chosen, AccessToken: tokens.AccessToken}, nil
}

// exhausted is where a utilisation figure stops counting as headroom. Not
// 100%: the figure is a poll rather than a live reading, and an account at
// 99.5% has nothing useful left before the next poll lands.

// pick returns the highest-priority account that can serve.
//
// Priority is the operator's explicit order, not something inferred. A personal
// subscription and a work one are not interchangeable, and spreading evenly is
// the wrong answer when one of them is the account you would rather not spend.
// So the list is walked from the top and the first account with room wins.
//
// Quota is a reason to skip, never a reason to reorder: Anthropic reports each
// account's 5-hour and 7-day utilisation on every response, so "out of room" is
// something we know rather than something we discover by being refused.
func (p *Pool) pick(candidates []store.Account) (store.Account, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()

	// First pass: the top account that is neither cooling down nor out.
	for _, a := range candidates {
		if p.stateOf(a.ID).cooldownUntil.After(now) || outOfRoom(a) {
			continue
		}
		return a, true
	}

	// Second pass: ignore the quota. Its figures are a snapshot and can be
	// stale or wrong, and an account that might be refused beats no account at
	// all — being told "no" by the upstream is a better outcome than inventing
	// one here.
	for _, a := range candidates {
		if p.stateOf(a.ID).cooldownUntil.After(now) {
			continue
		}
		return a, true
	}
	return store.Account{}, false
}

// outOfRoom reports whether the upstream has said this account cannot serve.
func outOfRoom(a store.Account) bool {
	if !a.Quota.Allowed() {
		return true
	}
	return a.Quota.Known() && a.Quota.Utilization() >= store.AtLimit
}

// AccessToken returns a usable token for one account, refreshing first if it
// is at or near expiry. Used by the status poll, which needs a specific
// account rather than whichever one the pool would pick.
func (p *Pool) AccessToken(ctx context.Context, id string) (string, error) {
	a, err := p.store.Account(ctx, id)
	if err != nil {
		return "", err
	}
	if p.now().Add(2 * time.Minute).After(a.ExpiresAt) {
		if err := p.Refresh(ctx, id); err != nil {
			return "", err
		}
	}
	tokens, err := p.store.AccountTokens(ctx, p.sealer, id)
	if err != nil {
		return "", err
	}
	return tokens.AccessToken, nil
}

// Refresh exchanges the refresh token, with one in-flight refresh per account.
//
// Concurrency matters more than it looks: providers rotate refresh tokens, so
// two simultaneous refreshes race and the loser's token is already spent.
func (p *Pool) Refresh(ctx context.Context, id string) error {
	p.mu.Lock()
	h := p.stateOf(id)
	if h.refreshing != nil {
		wait := h.refreshing
		p.mu.Unlock()
		select {
		case <-wait:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	done := make(chan struct{})
	h.refreshing = done
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		h.refreshing = nil
		p.mu.Unlock()
		close(done)
	}()

	tokens, err := p.store.AccountTokens(ctx, p.sealer, id)
	if err != nil {
		return err
	}
	res, err := oauth.RefreshAnthropic(ctx, p.client, tokens.RefreshToken)
	if err != nil {
		p.store.MarkAccountError(ctx, id, err.Error())
		return err
	}
	// Identity is never taken from a refresh response; only the tokens are.
	if err := p.store.UpdateAccountTokens(ctx, p.sealer, id,
		store.Tokens{AccessToken: res.AccessToken, RefreshToken: res.RefreshToken},
		res.ExpiresAt, res.RefreshTokenExpiresAt); err != nil {
		return err
	}

	p.mu.Lock()
	h.cooldownUntil = time.Time{}
	h.failures = 0
	p.mu.Unlock()

	attrs := []any{"account", id, "expires", res.ExpiresAt.UTC().Format(time.RFC3339)}
	if !res.RefreshTokenExpiresAt.IsZero() {
		left := time.Until(res.RefreshTokenExpiresAt)
		attrs = append(attrs, "reauth_in", left.Round(time.Hour).String())
		// Same three-day window the client warns in. Refreshing cannot push
		// this back — only a human at a browser can.
		if left <= 3*24*time.Hour {
			p.log.Warn("account needs re-authorisation soon; refreshing cannot extend it",
				"account", id, "reauth_in", left.Round(time.Hour).String())
		}
	}
	p.log.Info("token refreshed", attrs...)
	return nil
}

func (p *Pool) ReportSuccess(id string) {
	p.mu.Lock()
	h := p.stateOf(id)
	h.cooldownUntil = time.Time{}
	h.failures = 0
	p.mu.Unlock()
	p.store.MarkAccountUsed(context.Background(), id)
}

// ReportFailure cools an account down, backing off further each consecutive
// time so a persistently broken account stops being tried every request.
func (p *Pool) ReportFailure(id string, kind FailureKind, detail string) {
	p.mu.Lock()
	h := p.stateOf(id)
	h.failures++
	b, ok := backoff[kind]
	if !ok {
		b = backoff[FailureServer]
	}
	wait := b.base << min(h.failures-1, 16)
	if wait > b.max || wait <= 0 {
		wait = b.max
	}
	h.cooldownUntil = p.now().Add(wait)
	failures := h.failures
	p.mu.Unlock()

	p.store.MarkAccountError(context.Background(), id, string(kind)+": "+detail)
	p.log.Warn("account cooling down",
		"account", id, "kind", kind, "failures", failures, "for", wait.String())
}

// RetryAfter reports how long until some account frees up, for a Retry-After
// header when everything is cooling down.
func (p *Pool) RetryAfter() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	var soonest time.Duration
	for _, h := range p.states {
		if !h.cooldownUntil.After(now) {
			return 0
		}
		d := h.cooldownUntil.Sub(now)
		if soonest == 0 || d < soonest {
			soonest = d
		}
	}
	return soonest
}
