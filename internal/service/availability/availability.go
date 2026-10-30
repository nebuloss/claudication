// Package availability keeps, for every account, which of its models work
// there now, and routes around the ones that do not.
//
// An account's model list says what its plan has, not what it serves this
// minute: measured 2026-10-23, Google listed gemini-2.5-pro with quota left
// on a Google AI Pro account and refused every request for it ("No capacity
// available for model gemini-2.5-pro on the server") while the account's
// other models answered. Only a request tells, so the gateway makes them:
//
//   - When an account is added or reconnected, every model it lists is tried
//     at once (Scan), a few at a time: one tiny turn each on the account's
//     own credential, as the account page's Test is.
//   - A model that failed is tried again every Recheck, unless the upstream
//     said when it would be back — a quota's reset — and that is later.
//   - Client traffic counts, at no cost: a request refused for its model
//     (provider.Verdict Model, or a 404) marks it failing; one served marks
//     it working, and notes when it last did (written at most once a
//     SeenEvery per model, not once a request).
//
// Checking costs tokens on the operator's subscriptions, so it has a switch
// (Setting, on by default): off, nothing is checked — no scan, no recheck —
// while what costs nothing goes on: client traffic, and the Test button,
// still mark a model failing or working, and are still routed by.
//
// A model that keeps failing on an account for a reason that lasts — every
// check for AutoOffAfter, and not a rate limit or a spent quota, which come
// back on their own — is switched off there, as the operator's switch would
// (store.SwitchModelOffAuto): the owner's, 2026-10-26, for gemini-2.5-pro.
// It is checked on, and switched back on the first time it works; one the
// operator switched off is never touched. Only while checking is on: off,
// nothing would ever bring it back.
//
// A failing model is routed around: the pool holds that account off it
// (pool.HoldModel) until the next check, so a request for it goes to another
// account that has it working. Held is not refused — with no other account,
// the pool's last pass still serves from this one — so a stale failure never
// blocks a model outright. The last result of each, and when the model last
// worked, are account_model_tests in schema.sql, which the account's Models
// page shows.
package availability

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"claudication/internal/live"
	"claudication/internal/store"
)

// Setting is the switch's key in the settings table.
const Setting = "models.check"

const (
	// Recheck is how often a failing model is tried again (the operator's
	// choice, 2026-10-26: a turn of 10 to 70 tokens a quarter of an hour).
	Recheck = 15 * time.Minute
	// SeenEvery is how often a model a client keeps being served is written
	// down as working: the last-worked time is to the minute, not the request.
	SeenEvery = time.Minute
	// Tick is how often the failing models are looked over.
	Tick = time.Minute
	// AutoOffAfter is how long a model fails on an account, for a reason
	// that lasts, before it is switched off there: three checks.
	AutoOffAfter = 30 * time.Minute
	// scanAtOnce is how many of an account's models are tried at the same
	// time: the account page's Test all does as many.
	scanAtOnce = 4
	// scanWait bounds a whole scan, past the probes' own timeouts.
	scanWait = 10 * time.Minute
)

// Store is where each model's last result is kept, and its switch.
type Store interface {
	Account(ctx context.Context, id string) (store.Account, error)
	SaveModelTest(ctx context.Context, accountID string, t store.ModelTest) error
	FailingModelTests(ctx context.Context) (map[string][]store.ModelTest, error)
	SwitchModelOffAuto(ctx context.Context, id, model string) (bool, error)
	SwitchModelOnAuto(ctx context.Context, id, model string) (bool, error)
}

// Holds is the pool's per-model hold on an account.
type Holds interface {
	HoldModel(id, model string, until time.Time)
	ReleaseModel(id, model string)
	ModelHeldUntil(id, model string) time.Time
}

// ProbeFunc tries one model on one account, as the account page's Test does,
// and says how it went; the source is set here.
type ProbeFunc func(ctx context.Context, a store.Account, model string) store.ModelTest

// ModelsFunc is the models an account's own list names.
type ModelsFunc func(ctx context.Context, accountID string) ([]string, error)

// Switch is the operator's on and off for checking.
type Switch interface{ On() bool }

// Checker keeps the models' state. Every field but Log and Checks is
// required; a nil Checks checks.
type Checker struct {
	Store  Store
	Holds  Holds
	Probe  ProbeFunc
	Models ModelsFunc
	Checks Switch
	Log    *slog.Logger
	// Hub is told when a scan starts and ends: an account's page shows it.
	Hub *live.Hub

	mu sync.Mutex
	// failing is each failing pair, with when it last failed.
	failing map[pair]failed
	// seen is when a pair last had a client's success written down.
	seen map[pair]time.Time
	// trying are the pairs with a probe in flight, and scanning the accounts
	// with a scan in flight.
	trying   map[pair]bool
	scanning map[string]bool

	now func() time.Time
}

// pair is an account and a model, the model by store.ModelKey so a dated id
// and its alias are one.
type pair struct{ account, model string }

// failed is a failing pair: when it last failed, the model's id as it was
// asked for, which a check asks again, and when it began failing for a
// reason that lasts (zero while it has not), which AutoOffAfter counts from.
type failed struct {
	at    time.Time
	model string
	since time.Time
}

// lasting reports whether a failure is one that waiting does not cure: not a
// rate limit or a spent quota (429), which reset, and not one where the
// upstream was never reached (no status), which is the account's or the
// network's, not the model's.
func lasting(t store.ModelTest) bool {
	return t.Status != 0 && t.Status != 429
}

func (c *Checker) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// checking reports whether the gateway checks models itself now.
func (c *Checker) checking() bool { return c.Checks == nil || c.Checks.On() }

func (c *Checker) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

// init makes the maps; under c.mu.
func (c *Checker) init() {
	if c.failing == nil {
		c.failing, c.seen = map[pair]failed{}, map[pair]time.Time{}
		c.trying, c.scanning = map[pair]bool{}, map[string]bool{}
	}
}

// Load reads which models were failing when the gateway last ran, and holds
// each account off them until their next check: the pool's holds are kept in
// memory, and a restart would otherwise send traffic straight back to them.
func (c *Checker) Load(ctx context.Context) error {
	failing, err := c.Store.FailingModelTests(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	for account, tests := range failing {
		for _, t := range tests {
			f := failed{at: t.At, model: t.Model}
			if lasting(t) {
				f.since = t.At
			}
			c.failing[pair{account, store.ModelKey(t.Model)}] = f
			c.Holds.HoldModel(account, t.Model, t.At.Add(Recheck))
		}
	}
	return nil
}

// Record keeps a model's result on an account and routes by it: a failure
// holds the account off the model until its next check, a success ends that.
func (c *Checker) Record(ctx context.Context, accountID string, t store.ModelTest) {
	if err := c.Store.SaveModelTest(ctx, accountID, t); err != nil {
		c.log().Warn("could not keep a model's result", "account", accountID, "model", t.Model, "err", err)
	}
	p := pair{accountID, store.ModelKey(t.Model)}
	c.mu.Lock()
	c.init()
	prev, wasFailing := c.failing[p]
	if t.OK {
		delete(c.failing, p)
		c.seen[p] = t.At
		c.Holds.ReleaseModel(accountID, t.Model)
		c.mu.Unlock()
		if wasFailing {
			c.switchOn(ctx, accountID, t.Model)
		}
		return
	}
	f := failed{at: t.At, model: t.Model}
	if lasting(t) {
		f.since = t.At
		if wasFailing && !prev.since.IsZero() {
			f.since = prev.since
		}
	}
	c.failing[p] = f
	c.Holds.HoldModel(accountID, t.Model, t.At.Add(Recheck))
	c.mu.Unlock()
	if !f.since.IsZero() && t.At.Sub(f.since) >= AutoOffAfter && c.checking() {
		c.switchOff(ctx, accountID, t.Model, t.Error)
	}
}

// switchOff switches a model that keeps failing off on an account.
func (c *Checker) switchOff(ctx context.Context, accountID, model, why string) {
	off, err := c.Store.SwitchModelOffAuto(ctx, accountID, model)
	if err != nil {
		c.log().Warn("could not switch a failing model off", "account", accountID, "model", model, "err", err)
		return
	}
	if off {
		c.log().Warn("switched a model off on an account: it kept failing",
			"account", accountID, "model", model, "for", AutoOffAfter.String(), "error", why)
	}
}

// switchOn switches back on a model the gateway switched off, now it works.
func (c *Checker) switchOn(ctx context.Context, accountID, model string) {
	on, err := c.Store.SwitchModelOnAuto(ctx, accountID, model)
	if err != nil {
		c.log().Warn("could not switch a model back on", "account", accountID, "model", model, "err", err)
		return
	}
	if on {
		c.log().Info("switched a model back on: it works again", "account", accountID, "model", model)
	}
}

// Served notes a client request a model served on an account. Free when the
// model was already known to work and was noted within SeenEvery; otherwise
// written down, off the request's path.
func (c *Checker) Served(accountID, model string) {
	if model == "" {
		return
	}
	now := c.clock()
	p := pair{accountID, store.ModelKey(model)}
	c.mu.Lock()
	c.init()
	_, wasFailing := c.failing[p]
	if !wasFailing && now.Sub(c.seen[p]) < SeenEvery {
		c.mu.Unlock()
		return
	}
	c.seen[p] = now
	c.mu.Unlock()
	go c.Record(context.Background(), accountID, store.ModelTest{
		Model: model, At: now, OK: true, Status: 200, Source: store.TestByTraffic,
	})
}

// Refused notes a client request refused for its model on an account: the
// model is failing there until it is checked again. One refusal a minute is
// written down; a burst of them says nothing more.
func (c *Checker) Refused(accountID, model string, status int, detail string) {
	if model == "" {
		return
	}
	now := c.clock()
	p := pair{accountID, store.ModelKey(model)}
	c.mu.Lock()
	c.init()
	if f, ok := c.failing[p]; ok && now.Sub(f.at) < SeenEvery {
		c.mu.Unlock()
		return
	}
	f := failed{at: now, model: model}
	if prev, ok := c.failing[p]; ok {
		f.since = prev.since
	}
	c.failing[p] = f
	c.mu.Unlock()
	go c.Record(context.Background(), accountID, store.ModelTest{
		Model: model, At: now, Status: status, Error: detail, Source: store.TestByTraffic,
	})
}

// Scan tries every model an account lists, a few at a time, in the
// background: what an account just added or reconnected is checked with.
// Nothing while checking is switched off.
func (c *Checker) Scan(accountID string) {
	if !c.checking() {
		return
	}
	c.mu.Lock()
	c.init()
	if c.scanning[accountID] {
		c.mu.Unlock()
		return
	}
	c.scanning[accountID] = true
	c.mu.Unlock()
	c.Hub.Changed(live.Models)
	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.scanning, accountID)
			c.mu.Unlock()
			c.Hub.Changed(live.Models)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), scanWait)
		defer cancel()
		a, err := c.Store.Account(ctx, accountID)
		if err != nil || a.Disabled() {
			return
		}
		models, err := c.Models(ctx, accountID)
		if err != nil {
			c.log().Warn("could not list an account's models to check them", "account", a.Email, "err", err)
			return
		}
		sem := make(chan struct{}, scanAtOnce)
		var wg sync.WaitGroup
		for _, m := range models {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer func() { <-sem; wg.Done() }()
				c.try(ctx, a, m)
			}()
		}
		wg.Wait()
	}()
}

// Failing reports whether a model is failing on an account now: its last
// result, kept until a check or a client's request says otherwise.
func (c *Checker) Failing(accountID, model string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.failing[pair{accountID, store.ModelKey(model)}]
	return ok
}

// Scanning reports whether an account's models are being checked now.
func (c *Checker) Scanning(accountID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.scanning[accountID]
}

// try checks one model on one account and records what it found, unless a
// check of it is already in flight.
func (c *Checker) try(ctx context.Context, a store.Account, model string) {
	p := pair{a.ID, store.ModelKey(model)}
	c.mu.Lock()
	c.init()
	if c.trying[p] {
		c.mu.Unlock()
		return
	}
	c.trying[p] = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.trying, p)
		c.mu.Unlock()
	}()
	t := c.Probe(ctx, a, model)
	t.Model, t.Source = model, store.TestByScan
	if t.At.IsZero() {
		t.At = c.clock()
	}
	c.Record(ctx, a.ID, t)
}

// Run checks the failing models again as each comes due, until ctx ends or
// stop closes.
func (c *Checker) Run(ctx context.Context, stop <-chan struct{}) {
	ticker := time.NewTicker(Tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			c.recheck(ctx)
		}
	}
}

// recheck tries each failing model whose check is due: Recheck after it last
// failed, and not before the upstream said it would be back. One at a time:
// this is background upkeep, and nothing waits on it.
func (c *Checker) recheck(ctx context.Context) {
	if !c.checking() {
		return
	}
	now := c.clock()
	type check struct {
		p     pair
		model string
	}
	var due []check
	c.mu.Lock()
	c.init()
	for p, f := range c.failing {
		if now.Sub(f.at) >= Recheck && !c.Holds.ModelHeldUntil(p.account, f.model).After(now) {
			due = append(due, check{p, f.model})
		}
	}
	c.mu.Unlock()
	for _, d := range due {
		p := d.p
		if ctx.Err() != nil {
			return
		}
		a, err := c.Store.Account(ctx, p.account)
		if err != nil {
			// The account is gone, and its models with it.
			c.mu.Lock()
			delete(c.failing, p)
			c.mu.Unlock()
			continue
		}
		if a.Disabled() {
			continue
		}
		c.try(ctx, a, d.model)
	}
}
