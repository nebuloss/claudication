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
	// ErrModelOff is a request for a model that every account able to serve
	// has turned off. Not a cooldown: waiting does not bring it back, an
	// operator does, so it is answered as a model this gateway does not serve.
	ErrModelOff = errors.New("no account has this model turned on")
)

// Lease is one account, ready to serve a request.
type Lease struct {
	Account     store.Account
	AccessToken string
	// Model is the version this account serves, for a request any of
	// several could (WithVersions): its newest of them. "" otherwise, the
	// request's own model being the one.
	Model string
	// release takes this request off the account's in-flight count. Nil on
	// a lease the pool did not hand out, which a test's fake pool returns.
	release func()
}

// Release says the request this lease was taken for is done with the
// account: answered, abandoned for another, or failed. Safe to call more than
// once and on the zero Lease, so a caller can release on every path without
// tracking which ones already did.
func (l Lease) Release() {
	if l.release != nil {
		l.release()
	}
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
	client *http.Client
	log    *slog.Logger

	mu     sync.Mutex
	states map[string]*health
	now    func() time.Time

	// refreshers are the token refresh itself, one per provider, injectable
	// so the cancellation and error-propagation rules around it can be tested
	// without a live provider. The rules are the part that has teeth: getting
	// them wrong takes an account offline until a human redoes the browser
	// flow.
	refreshers map[string]RefreshFunc

	// affinity is which account last served each conversation, for the
	// providers that want one kept; see affinity.go.
	affinity affinity

	// quotaSeen is what each account's live quota last wrote, so a figure
	// read off every relayed answer is not a database write per request; see
	// NoteQuota. quotaWrites counts the writes in flight.
	quotaSeen   map[string]quotaNote
	quotaWrites sync.WaitGroup

	// open counts the leases each account has out, under mu: what the
	// account's page shows as in flight. Counted here rather than at the
	// gateway, because only the pool knows which account a request went to
	// before it is answered — and a stream holds its account for minutes.
	open map[string]int

	// lockRefresh, when set, is held around each token exchange across
	// processes; see LockRefreshes.
	lockRefresh func(ctx context.Context, id string) (unlock func(), err error)
}

// LockRefreshes has every token exchange hold lock, keyed by account id, from
// reading the refresh token to storing its replacement.
//
// Refresh already lets one exchange per account run in this process. During
// a handover two processes serve from one database for up to the shutdown
// grace (internal/handover), and both reading the same refresh token and
// exchanging it is the race Refresh exists to prevent: the provider rotates
// it, and the slower process spends a token that is already spent, which
// takes the account offline until someone signs it in again. The second
// exchange, once the first has stored its result, reads the new token and is
// merely redundant.
func (p *Pool) LockRefreshes(lock func(ctx context.Context, id string) (unlock func(), err error)) {
	p.lockRefresh = lock
}

// RefreshFunc trades a refresh token for new tokens at the provider. The pool
// decides when to refresh and what to do with the answer; how is the
// provider's, which is why it is handed in rather than known here.
type RefreshFunc func(ctx context.Context, client *http.Client, refreshToken string) (oauth.Result, error)

// New returns a pool over the accounts in st, refreshing each account's token
// with its provider's function in refresh.
//
// One pool for every provider: which accounts may serve is already asked by
// provider (candidatesFor), and one priority list, one set of cooldowns and
// one refresh lock per account are the same thing whoever the account
// belongs to.
func New(st *store.Store, client *http.Client, log *slog.Logger, refresh map[string]RefreshFunc) *Pool {
	refreshers := make(map[string]RefreshFunc, len(refresh))
	for k, v := range refresh {
		refreshers[k] = v
	}
	return &Pool{
		store:      st,
		client:     client,
		log:        log,
		states:     make(map[string]*health),
		now:        time.Now,
		refreshers: refreshers,
		affinity:   affinity{last: map[string]affine{}},
		quotaSeen:  map[string]quotaNote{},
		open:       map[string]int{},
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
	// InFlight counts the requests each account is serving now, by the
	// leases out on it. Absent means none.
	InFlight map[string]int
}

// Status reports what the pool would do right now, without doing it.
func (p *Pool) Status(ctx context.Context, provider string) (Status, error) {
	accounts, err := p.store.ListAccounts(ctx)
	if err != nil {
		return Status{}, err
	}
	candidates := candidatesFor(accounts, provider, "", nil, p.now())

	st := Status{Cooling: map[string]time.Time{}, InFlight: map[string]int{}}
	for _, a := range accounts {
		if a.Provider == provider && a.NeedsReauth() {
			st.NeedsReauth++
		}
	}

	p.mu.Lock()
	for _, a := range accounts {
		// Every account of the provider, not only the candidates: one just
		// paused can still be finishing a stream it started.
		if n := p.open[a.ID]; a.Provider == provider && n > 0 {
			st.InFlight[a.ID] = n
		}
	}
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
//
// model narrows it to the accounts that have that model on; empty asks about
// the account alone. See store.Account.Serves.
func candidatesFor(accounts []store.Account, provider, model string, exclude map[string]bool, now time.Time) []store.Account {
	out := make([]store.Account, 0, len(accounts))
	for _, a := range accounts {
		if a.Provider != provider || a.Disabled() || exclude[a.ID] || !a.Serves(model) {
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

// Offers reports whether any usable account has model on — whether a request
// for it could be served at all, cooling aside. It is how a virtual model
// chooses among its targets: by the same candidates Acquire would pick from,
// so the two cannot disagree about a model being available.
func (p *Pool) Offers(ctx context.Context, provider, model string) bool {
	accounts, err := p.store.ListAccounts(ctx)
	if err != nil {
		return false
	}
	if v, ok := versionsOf(ctx); ok {
		c, _ := byVersion(candidatesFor(accounts, provider, "", nil, p.now()), v)
		return len(c) > 0
	}
	return len(candidatesFor(accounts, provider, model, nil, p.now())) > 0
}

// Acquire picks an account that has model on, refreshing its token first if
// it has expired. An empty model is served by any account. A request marked
// WithVersions is served by an account that has any of them, the lease saying
// which (Lease.Model), and model is not read.
//
// exclude lets a retry skip accounts that already failed for this request.
func (p *Pool) Acquire(ctx context.Context, provider, model string, exclude map[string]bool) (Lease, error) {
	accounts, err := p.store.ListAccounts(ctx)
	if err != nil {
		return Lease{}, err
	}

	now := p.now()
	var best map[string]string
	candidates := candidatesFor(accounts, provider, model, exclude, now)
	if v, ok := versionsOf(ctx); ok {
		candidates, best = byVersion(candidatesFor(accounts, provider, "", exclude, now), v)
	}
	if len(candidates) == 0 {
		switch {
		case len(exclude) > 0:
			// Every account that has the model on was tried.
			return Lease{}, ErrAllCoolingUp
		case len(candidatesFor(accounts, provider, "", nil, now)) > 0:
			// There are accounts; none has this model on.
			return Lease{}, ErrModelOff
		}
		return Lease{}, ErrNoAccounts
	}

	conversation := conversationOf(ctx)
	chosen, ok := p.preferred(provider, conversation, candidates)
	if !ok {
		chosen, ok = p.pick(candidates)
	}
	if !ok {
		return Lease{}, ErrAllCoolingUp
	}
	p.remember(provider, conversation, chosen.ID)

	if p.refreshDue(chosen) {
		if err := p.Refresh(ctx, chosen.ID); err != nil {
			p.ReportFailure(chosen.ID, FailureAuth, err.Error())
			return Lease{}, fmt.Errorf("refresh %s: %w", chosen.Email, err)
		}
		if chosen, err = p.store.Account(ctx, chosen.ID); err != nil {
			return Lease{}, err
		}
	}

	tokens, err := p.store.AccountTokens(ctx, chosen.ID)
	if err != nil {
		return Lease{}, err
	}
	return Lease{Account: chosen, AccessToken: tokens.AccessToken, Model: best[chosen.ID], release: p.hold(chosen.ID)}, nil
}

// hold counts one more request open on an account, and returns what takes it
// off again — once, however many times it is called.
func (p *Pool) hold(id string) func() {
	p.mu.Lock()
	if p.open == nil {
		p.open = map[string]int{} // a Pool built without New, as tests do
	}
	p.open[id]++
	p.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			if p.open[id]--; p.open[id] <= 0 {
				delete(p.open, id)
			}
			p.mu.Unlock()
		})
	}
}

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

// refreshAhead is how long before its expiry a token is refreshed, so a
// request never rides a token that dies mid-flight.
const refreshAhead = 2 * time.Minute

// refreshDue reports whether an account's token is at or near expiry. One
// rule for the lease and the status poll, which each spelled it out.
func (p *Pool) refreshDue(a store.Account) bool {
	return p.now().Add(refreshAhead).After(a.ExpiresAt)
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
	if p.refreshDue(a) {
		if err := p.Refresh(ctx, id); err != nil {
			return "", err
		}
	}
	tokens, err := p.store.AccountTokens(ctx, id)
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
	// lockWait bounds waiting for another process's exchange to finish: its
	// exchange and its write, and a little over.
	lockWait = refreshTimeout + persistTimeout + 10*time.Second
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
	before, err := p.store.Account(ctx, id)
	beforeOK := err == nil
	if beforeOK && before.RefreshDead() {
		return fmt.Errorf("%w: %s", ErrNeedsReauth, before.Email)
	}

	// Detached: see Refresh. Values carried on the context — request id,
	// tracing — are kept; the caller's ability to abandon this halfway
	// through is not.
	ctx = context.WithoutCancel(ctx)

	// Before the token is read, so the one read is the latest stored, and on
	// a budget of its own: waiting out another process's exchange must not
	// eat into this one's, which, cut off after the provider has rotated the
	// token, loses the only copy of the new one.
	if p.lockRefresh != nil {
		lockCtx, cancelLock := context.WithTimeout(ctx, lockWait)
		unlock, err := p.lockRefresh(lockCtx, id)
		cancelLock()
		defer unlock()
		if err != nil {
			return err
		}
	}

	exchangeCtx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()

	acct, err := p.store.Account(exchangeCtx, id)
	if err != nil {
		return err
	}
	// Whoever held the lock may have done this already. Dead is dead; a
	// token stored after the one this refresh set out to replace is the
	// refresh, done, and exchanging again would rotate it for nothing.
	if acct.RefreshDead() {
		return fmt.Errorf("%w: %s", ErrNeedsReauth, acct.Email)
	}
	if p.lockRefresh != nil && beforeOK && acct.ExpiresAt.After(before.ExpiresAt) && !p.refreshDue(acct) {
		p.mu.Lock()
		h.cooldownUntil = time.Time{}
		h.failures = 0
		p.mu.Unlock()
		return nil
	}
	exchange := p.refreshers[acct.Provider]
	if exchange == nil {
		return fmt.Errorf("no token refresh for provider %q", acct.Provider)
	}
	tokens, err := p.store.AccountTokens(exchangeCtx, id)
	if err != nil {
		return err
	}
	res, err := exchange(exchangeCtx, p.client, tokens.RefreshToken)
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
	if err := p.store.UpdateAccountTokens(persistCtx, id,
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

// Reset forgets an account's cooldown and failure count, for when its
// credentials have just been replaced.
//
// The backoff is earned by the old credentials. An account reconnected after
// its refresh token died had been failing on every attempt, so without this it
// would sit out up to the longest backoff with a token that works — invisible
// in the store, and exactly the account the operator just fixed.
//
// The backoff only: a refresh still in flight keeps its single-flight record,
// because dropping it would let a second refresh start beside it.
func (p *Pool) Reset(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if h, ok := p.states[id]; ok {
		h.cooldownUntil = time.Time{}
		h.failures = 0
	}
}

// ReportFailure cools an account down, backing off further each consecutive
// time so a persistently broken account stops being tried every request.
func (p *Pool) ReportFailure(id string, kind FailureKind, detail string) {
	p.ReportFailureUntil(id, kind, detail, time.Time{})
}

// maxReportedWait bounds how far ahead an upstream's own reset may sit an
// account out. A weekly limit is a week; a date further out than that is a
// figure gone wrong, and trusting it would take an account out of rotation
// until someone noticed.
const maxReportedWait = 8 * 24 * time.Hour

// ReportFailureUntil is ReportFailure for an upstream that said when the
// account can serve again — ChatGPT's usage_limit_reached carries the reset.
// The account sits out until then or for the usual backoff, whichever is
// longer: the backoff still grows for an account that keeps failing, and a
// reset already past is no reason to try it again at once.
//
// Sitting out is a preference, not a refusal: with every account out, the
// pool still serves from the one that comes back soonest (see pick), and the
// upstream's own refusal, naming the reset, is what the client reads.
func (p *Pool) ReportFailureUntil(id string, kind FailureKind, detail string, until time.Time) {
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
	now := p.now()
	if reported := until.Sub(now); reported > wait {
		wait = min(reported, maxReportedWait)
	}
	h.cooldownUntil = now.Add(wait)
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
//
// Only the accounts Acquire would offer count. The cooldowns live in memory
// and outlive the accounts they belong to, so reading them alone let a paused
// or deleted account whose cooldown had run out answer "free now" — and the
// client was told to retry at once into the same refusal.
func (p *Pool) RetryAfter(ctx context.Context, provider, model string) time.Duration {
	accounts, err := p.store.ListAccounts(ctx)
	if err != nil {
		return 0
	}
	candidates := candidatesFor(accounts, provider, model, nil, p.now())
	if v, ok := versionsOf(ctx); ok {
		candidates, _ = byVersion(candidatesFor(accounts, provider, "", nil, p.now()), v)
	}
	return p.soonest(candidates)
}

// soonest is how long until the first of candidates is out of its cooldown,
// or 0 when one already is.
func (p *Pool) soonest(candidates []store.Account) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	var soonest time.Duration
	for _, a := range candidates {
		h, ok := p.states[a.ID]
		if !ok || !h.cooldownUntil.After(now) {
			return 0
		}
		d := h.cooldownUntil.Sub(now)
		if soonest == 0 || d < soonest {
			soonest = d
		}
	}
	return soonest
}
