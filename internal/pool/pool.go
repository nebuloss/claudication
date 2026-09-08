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

	"claudication/internal/oauth"
	"claudication/internal/secret"
	"claudication/internal/store"
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
	// ErrNeedsReauth is returned instead of attempting a refresh that has
	// already been refused for good. Retrying invalid_grant is a loop, not
	// patience.
	ErrNeedsReauth = errors.New("this account needs re-authorising in a browser")
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
	refreshing *refreshCall
}

// refreshCall is one in-flight refresh. The error is written before done is
// closed and read after it, so waiters learn what actually happened instead of
// being told it succeeded and then handed the old, expired token.
type refreshCall struct {
	done chan struct{}
	err  error
}

type Pool struct {
	store  *store.Store
	sealer *secret.Sealer
	client *http.Client
	log    *slog.Logger

	mu     sync.Mutex
	states map[string]*health
	now    func() time.Time

	// exchange is the token refresh itself, injectable so the cancellation and
	// error-propagation rules around it can be tested without a live provider.
	// The rules are the part that has teeth: getting them wrong takes an
	// account offline until a human redoes the browser flow.
	exchange func(ctx context.Context, client *http.Client, refreshToken string) (oauth.Result, error)
}

func New(st *store.Store, sealer *secret.Sealer, client *http.Client, log *slog.Logger) *Pool {
	return &Pool{
		store:    st,
		sealer:   sealer,
		client:   client,
		log:      log,
		states:   make(map[string]*health),
		now:      time.Now,
		exchange: oauth.RefreshAnthropic,
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

// Status is the pool's own view of the accounts, which the store does not have:
// cooldowns live in memory and vanish with the process.
//
// It exists because the admin API was answering both "is the gateway ready"
// and "which account is serving" from the store alone. That made the readiness
// badge unable to see a cooling account at all, and made the serving badge a
// second, drifting implementation of pick(). Both questions are the pool's to
// answer.
type Status struct {
	// Serving is the account that would take the next request, or "" if none
	// would. Decided by pick(), so it cannot disagree with what Acquire does.
	Serving string
	// Cooling says when each sidelined account comes back. Absent means
	// healthy.
	Cooling map[string]time.Time
	// Usable counts accounts that are neither disabled, cooling, nor out of
	// room — the ones that could serve without the pool having to fall back.
	Usable int
	// NeedsReauth counts accounts no amount of waiting will recover: a refresh
	// token refused for good, or a refresh window that has closed.
	NeedsReauth int
}

// Status reports what the pool would do right now, without doing it.
func (p *Pool) Status(ctx context.Context, provider string) (Status, error) {
	accounts, err := p.store.ListAccounts(ctx)
	if err != nil {
		return Status{}, err
	}
	candidates := candidatesFor(accounts, provider, nil, p.now())

	st := Status{Cooling: map[string]time.Time{}}
	for _, a := range accounts {
		if a.Provider == provider && a.NeedsReauth() {
			st.NeedsReauth++
		}
	}

	p.mu.Lock()
	now := p.now()
	for _, a := range candidates {
		until := p.stateOf(a.ID).cooldownUntil
		if until.After(now) {
			st.Cooling[a.ID] = until
			continue
		}
		if !outOfRoom(a) {
			st.Usable++
		}
	}
	p.mu.Unlock()

	if chosen, ok := p.pick(candidates); ok {
		st.Serving = chosen.ID
	}
	return st, nil
}

// candidatesFor is the one place that decides which accounts are in play, so
// Acquire and Status cannot answer differently.
func candidatesFor(accounts []store.Account, provider string, exclude map[string]bool, now time.Time) []store.Account {
	out := make([]store.Account, 0, len(accounts))
	for _, a := range accounts {
		if a.Provider != provider || a.Disabled() || exclude[a.ID] {
			continue
		}
		// A dead refresh token still leaves whatever is left of the access
		// token, which can be hours — free service while the operator notices.
		// Once that is gone the account genuinely cannot serve, and offering it
		// only produces a failed refresh per request.
		if a.RefreshDead() && !now.Before(a.ExpiresAt) {
			continue
		}
		out = append(out, a)
	}
	return out
}

// Acquire picks an account, refreshing its token first if it has expired.
//
// exclude lets a retry skip accounts that already failed for this request.
func (p *Pool) Acquire(ctx context.Context, provider string, exclude map[string]bool) (Lease, error) {
	accounts, err := p.store.ListAccounts(ctx)
	if err != nil {
		return Lease{}, err
	}

	candidates := candidatesFor(accounts, provider, exclude, p.now())
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

	// Third pass: everything is cooling down. Serve from whichever recovers
	// soonest rather than refusing.
	//
	// The same argument as the quota pass, and observed rather than assumed:
	// the upstream returned 429 and then served a 200 half a second later, so
	// a rate-limit refusal is a statement about one request, not about the
	// next minute. Cooling an account down is the right way to *prefer*
	// another one; with a single account it would otherwise turn a blip into
	// an outage, and hand the client our invented overloaded_error instead of
	// the upstream's own 429 — which is the body Claude Code reads to decide
	// whether to retry.
	best, found := store.Account{}, false
	var soonest time.Time
	for _, a := range candidates {
		until := p.stateOf(a.ID).cooldownUntil
		if !found || until.Before(soonest) {
			best, soonest, found = a, until, true
		}
	}
	return best, found
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

// refreshTimeout bounds the token exchange, and persistTimeout the write that
// stores its result. The write gets its own budget rather than the remainder of
// the exchange's, because it is the one that must not be abandoned.
const (
	refreshTimeout = 60 * time.Second
	persistTimeout = 30 * time.Second
)

// Refresh exchanges the refresh token, with one in-flight refresh per account.
//
// Concurrency matters more than it looks: providers rotate refresh tokens, so
// two simultaneous refreshes race and the loser's token is already spent.
//
// Cancellation matters for exactly the same reason, and is the sharper edge.
// Anthropic rotates the refresh token on every exchange, so between the
// upstream returning and the new token reaching the database there is a window
// where the only usable credential for this account exists in this process'
// memory. If the caller's context ends inside it — a client pressing Esc, a
// shutdown, a request deadline — the old token is already spent upstream and
// the new one is lost, and the account stays dead until a human redoes the
// browser flow. So the exchange and the write run detached from the caller,
// bounded by their own deadlines instead. A refresh is the pool's work, not the
// requester's.
func (p *Pool) Refresh(ctx context.Context, id string) error {
	p.mu.Lock()
	h := p.stateOf(id)
	if call := h.refreshing; call != nil {
		p.mu.Unlock()
		select {
		case <-call.done:
			// Report what the refresh actually did. Returning nil here hands
			// the waiter a token that was never replaced, and then blames it
			// for the 401 that follows.
			return call.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	call := &refreshCall{done: make(chan struct{})}
	h.refreshing = call
	p.mu.Unlock()

	err := p.refresh(ctx, id, h)

	p.mu.Lock()
	h.refreshing = nil
	p.mu.Unlock()
	call.err = err
	close(call.done)
	return err
}

func (p *Pool) refresh(ctx context.Context, id string, h *health) error {
	// Established dead: do not ask again. The upstream's answer will not have
	// changed, and asking costs an error line, a cooldown, and five minutes
	// later the same again.
	if a, err := p.store.Account(ctx, id); err == nil && a.RefreshDead() {
		return fmt.Errorf("%w: %s", ErrNeedsReauth, a.Email)
	}

	// Detached: see Refresh. Values carried on the context — request id,
	// tracing — are kept; the caller's ability to abandon this halfway
	// through is not.
	ctx = context.WithoutCancel(ctx)

	exchangeCtx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()

	tokens, err := p.store.AccountTokens(exchangeCtx, p.sealer, id)
	if err != nil {
		return err
	}
	res, err := p.exchange(exchangeCtx, p.client, tokens.RefreshToken)
	if err != nil {
		// invalid_grant is terminal: revoked, expired, or already spent.
		// Record it so nothing tries again, and say plainly what fixes it,
		// because nothing this process does will.
		if errors.Is(err, oauth.ErrInvalidGrant) {
			if derr := p.store.MarkRefreshDead(exchangeCtx, id, err.Error()); derr != nil {
				p.log.Error("could not record a dead refresh token", "account", id, "err", derr)
			}
			p.log.Error("refresh token refused for good; this account needs re-authorising in the admin UI",
				"account", id, "err", err)
			return fmt.Errorf("%w: %v", ErrNeedsReauth, err)
		}
		p.store.MarkAccountError(exchangeCtx, id, err.Error())
		return err
	}

	// Past this line the old refresh token is spent and only the response
	// holds a usable one. Fresh budget, and nothing that can cancel it.
	persistCtx, cancelPersist := context.WithTimeout(ctx, persistTimeout)
	defer cancelPersist()

	// Identity is never taken from a refresh response; only the tokens are.
	if err := p.store.UpdateAccountTokens(persistCtx, p.sealer, id,
		store.Tokens{AccessToken: res.AccessToken, RefreshToken: res.RefreshToken},
		res.ExpiresAt, res.RefreshTokenExpiresAt); err != nil {
		// The account is now in the state this function exists to avoid. Say
		// so plainly: nothing but a human at a browser can fix it.
		p.log.Error("token refreshed upstream but could not be stored; this account now needs re-authorisation",
			"account", id, "err", err)
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

// bookkeepingTimeout bounds the "how did that go" writes, which happen on the
// request path but belong to nobody's request. context.Background() would be
// wrong twice over: database/sql waits for a free connection with no deadline
// at all, so a slow admin query holding the pool could stall a relay
// indefinitely, and there would be no upper bound on how long a handler sits
// here after its client has already been answered.
const bookkeepingTimeout = 5 * time.Second

func (p *Pool) ReportSuccess(id string) {
	p.mu.Lock()
	h := p.stateOf(id)
	h.cooldownUntil = time.Time{}
	h.failures = 0
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), bookkeepingTimeout)
	defer cancel()
	p.store.MarkAccountUsed(ctx, id)
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

	ctx, cancel := context.WithTimeout(context.Background(), bookkeepingTimeout)
	defer cancel()
	p.store.MarkAccountError(ctx, id, string(kind)+": "+detail)
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
