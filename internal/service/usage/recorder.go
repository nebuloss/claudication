// Package usage records what each request cost and keeps that history
// bounded.
//
// Recording is bookkeeping: best-effort, never on a caller's critical path,
// and a failure costs a row in a chart rather than the request. What a failure
// is *called* is decided here, once, for every row — the provider supplies the
// classifier — so the request log's column, its filter menu and its filter
// cannot drift into three vocabularies.
package usage

import (
	"context"
	"log/slog"
	"time"

	"claudication/internal/store"
)

// Store is where history lives. The database is one.
type Store interface {
	RecordUsage(ctx context.Context, e store.UsageEvent) error
	PruneUsage(ctx context.Context, keep time.Duration) (int64, error)
	PruneChatTitles(ctx context.Context) (int64, error)
}

// Crediter is told what a request cost, for a key with a budget: the budget
// tracker, which keeps its cached figure current between refreshes.
type Crediter interface {
	Add(keyID string, tokens int64)
}

// Recorder files requests and prunes what has aged out.
type Recorder struct {
	Store Store
	// Retention is how long history is kept; zero records nothing.
	Retention time.Duration
	// Classify names a failure in one word from its status and the upstream's
	// text — the provider's vocabulary. Nil leaves ErrorCode as given.
	Classify func(status int, errText string) string
	// Budgets is credited with each request's tokens; nil for none.
	Budgets Crediter
	Log     *slog.Logger
}

// Enabled reports whether requests are recorded at all.
func (r *Recorder) Enabled() bool { return r.Retention > 0 }

func (r *Recorder) log() *slog.Logger {
	if r.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return r.Log
}

// Record files one request.
//
// It runs with its own context because the request's is cancelled the moment
// the client hangs up — which is exactly when a failed stream is most worth
// recording. budget is the key's ceiling, so an unlimited key skips the
// tracker entirely rather than taking its lock to find nothing to update.
func (r *Recorder) Record(e store.UsageEvent, budget int64) {
	if !r.Enabled() {
		return
	}
	if r.Classify != nil {
		e.ErrorCode = r.Classify(e.Status, e.Error)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Store.RecordUsage(ctx, e); err != nil {
		r.log().Warn("could not record usage", "err", err)
		return
	}
	// Credit the budget tracker with what this cost, so the cached figure keeps
	// up between refreshes rather than letting a burst through on a reading
	// taken half a minute ago. Only for a key that has a ceiling: for every
	// other key there is nothing to keep up with.
	if budget > 0 && e.KeyID != "" && r.Budgets != nil {
		r.Budgets.Add(e.KeyID, e.Tokens())
	}
}

// Prune drops history older than the retention, and the chat titles whose
// conversations went with it.
//
// Titles are keyed on conversations that only exist as events, so they are
// pruned here rather than on a schedule of their own: run separately they
// would drift, and a title outliving its events is a name for something
// nobody can look at.
func (r *Recorder) Prune(ctx context.Context) {
	n, err := r.Store.PruneUsage(ctx, r.Retention)
	if err != nil {
		r.log().Warn("could not prune usage", "err", err)
		return
	}
	if n > 0 {
		r.log().Info("pruned usage events", "rows", n,
			"older_than_days", int(r.Retention.Hours()/24))
	}
	if n, err := r.Store.PruneChatTitles(ctx); err != nil {
		r.log().Warn("could not prune chat titles", "err", err)
	} else if n > 0 {
		r.log().Info("pruned chat titles", "rows", n)
	}
}

// RunPruner prunes once at startup — a gateway that was down for a month
// should not wait another day to catch up — then every interval until stop
// closes. Retention is the only thing bounding the event table, which grows
// for as long as the gateway is used.
func (r *Recorder) RunPruner(stop <-chan struct{}, every time.Duration) {
	if !r.Enabled() {
		return
	}
	prune := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		r.Prune(ctx)
	}
	prune()
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			prune()
		}
	}
}
