// Package uptime is the gateway's record of its own availability, and the
// public summary of it: the status page at the relay's root, and the history
// on the admin Overview.
//
// # What is recorded
//
// Once a minute the gateway notes which of three states it is in (Probe,
// supplied by the composition root, decides):
//
//   - up: the gateway switch is on and at least one account could take a
//     request now — not disabled, not cooling down, not out of room;
//   - degraded: the switch is on and no account could, so a request either
//     fails or rides an account the pool would rather not use;
//   - paused: the operator switched the gateway off. Its own state rather
//     than degraded, because it is deliberate: the page says so, and the
//     uptime figures leave it out, the way a status page leaves out a
//     maintenance window.
//
// Down is never recorded, because a process that is not running records
// nothing. It is read from the gaps instead: two samples further apart than
// Tolerance mean the gateway was not running in between. A restart for a
// deploy takes seconds and costs at most a missed minute, which the tolerance
// absorbs, so it does not paint a day red; a crash that waits for somebody
// to notice does.
//
// Not recorded: anything about why. A failure rate from the request log would
// make "degraded" sharper, but every way of deriving it publishes something
// about traffic — that there was any, at minimum — and the public page is
// held to saying nothing about traffic at all.
//
// # What is published
//
// Report is everything GET /status.json answers, and the whole of it is
// derived from the three states and the clock. Each field and why it is safe
// to show to anyone is in docs/status-page.md; the short form is that the
// page tells a passer-by whether the gateway was up, and nothing about what
// it serves, to whom, or how much.
package uptime

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"claudication/internal/store"
)

// State is how the gateway was in a stretch of time. The numbers are what
// uptime_samples stores, and their order is badness: a bucket is drawn in
// the worst state it contains.
type State int

const (
	// None is a stretch before the history begins: no claim either way.
	None State = iota
	Up
	Paused
	Degraded
	// Down is a gap between samples longer than Tolerance. Never stored.
	Down
)

func (s State) String() string {
	switch s {
	case Up:
		return "up"
	case Paused:
		return "paused"
	case Degraded:
		return "degraded"
	case Down:
		return "down"
	}
	return "none"
}

const (
	// Every is how often a sample is taken. A minute is the finest anyone
	// reads an uptime at, and 90 days of it is 130,000 rows of a dozen bytes.
	Every = time.Minute

	// Tolerance is the longest gap between samples that is still read as the
	// gateway running. Five minutes: a deploy's restart misses one sample at
	// most, and an outage shorter than this is one nobody can act on before
	// it is over.
	Tolerance = 5 * time.Minute

	// History is how far back the report reaches, and how long samples are
	// kept at least, whatever the usage retention says: the retention bounds
	// the per-request record, which is large and personal, and a 90-day bar
	// that can only ever fill 30 days of itself would look like 60 days of
	// missing data.
	History = 90 * 24 * time.Hour

	// cacheFor is how long a report is reused. /status.json is public and
	// unauthenticated, so what one anonymous request costs bounds what a
	// flood of them does; a minute is also how often the data changes.
	cacheFor = time.Minute

	// maxIncidents caps the list. The page shows the recent ones, and a
	// flapping account must not turn the answer into a log.
	maxIncidents = 20
)

// Store is where the samples live. The database is one.
type Store interface {
	RecordUptime(ctx context.Context, at time.Time, state int) error
	UptimeSince(ctx context.Context, from time.Time) ([]store.UptimeSample, error)
	PruneUptime(ctx context.Context, before time.Time) (int64, error)
}

// Sampler takes the samples, prunes them, and answers the report.
type Sampler struct {
	Store Store
	// Probe says what state the gateway is in now. Up, Paused or Degraded;
	// anything else is recorded as Degraded.
	Probe func(ctx context.Context) State
	// Retention is the usage retention; samples are kept for the longer of
	// it and History.
	Retention time.Duration
	Log       *slog.Logger
	// Now is the clock, for tests. Nil is time.Now.
	Now func() time.Time

	mu       sync.Mutex
	cached   Report
	cachedAt time.Time
}

func (s *Sampler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Sampler) probe(ctx context.Context) State {
	switch st := s.Probe(ctx); st {
	case Up, Paused, Degraded:
		return st
	}
	return Degraded
}

// Sample records the state the gateway is in now.
func (s *Sampler) Sample(ctx context.Context) error {
	return s.Store.RecordUptime(ctx, s.now(), int(s.probe(ctx)))
}

// Prune drops the samples older than the longer of the retention and History.
func (s *Sampler) Prune(ctx context.Context) (int64, error) {
	keep := max(s.Retention, History)
	return s.Store.PruneUptime(ctx, s.now().Add(-keep))
}

// Run samples at once and then every Every, and prunes daily, until stop.
// The first sample is taken on start rather than a minute in, so the minute
// a restart lands in is never a gap of its own.
func (s *Sampler) Run(stop <-chan struct{}) {
	ctx := context.Background()
	sample := func() {
		if err := s.Sample(ctx); err != nil {
			s.Log.Warn("could not record uptime", "err", err)
		}
	}
	prune := func() {
		if _, err := s.Prune(ctx); err != nil {
			s.Log.Warn("could not prune uptime", "err", err)
		}
	}
	sample()
	prune()
	tick := time.NewTicker(Every)
	defer tick.Stop()
	day := time.NewTicker(24 * time.Hour)
	defer day.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			sample()
		case <-day.C:
			prune()
		}
	}
}

// Report is the summary, cached for a minute.
func (s *Sampler) Report(ctx context.Context) (Report, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cachedAt.IsZero() && now.Sub(s.cachedAt) < cacheFor && !now.Before(s.cachedAt) {
		return s.cached, nil
	}
	// Tolerance of slack before the window, so a sample just outside it still
	// says whether the window's first minutes were covered.
	samples, err := s.Store.UptimeSince(ctx, now.Add(-History-Tolerance))
	if err != nil {
		return Report{}, err
	}
	s.cached, s.cachedAt = Build(samples, now, s.probe(ctx)), now
	return s.cached, nil
}
