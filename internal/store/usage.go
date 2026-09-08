package store

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"
)

// UsageEvent is one proxied request, as it happened.
type UsageEvent struct {
	At               time.Time
	KeyID            string
	KeyName          string
	AccountID        string
	AccountEmail     string
	Model            string
	Path             string
	Status           int
	Streaming        bool
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	Duration         time.Duration
	Error            string
}

// Tokens is the billable total: cache reads are counted because they are
// charged, at a discount the gateway has no way to know.
func (e UsageEvent) Tokens() int64 {
	return int64(e.InputTokens) + int64(e.OutputTokens) +
		int64(e.CacheReadTokens) + int64(e.CacheWriteTokens)
}

// RecordUsage appends one event.
//
// It never returns an error to the request path — see the caller. Recording is
// bookkeeping, and a full disk should degrade the statistics, not the proxy.
func (s *Store) RecordUsage(ctx context.Context, e UsageEvent) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO usage_events (at, key_id, key_name, account_id, account_email,
		                           model, path, status, streaming,
		                           input_tokens, output_tokens,
		                           cache_read_tokens, cache_write_tokens,
		                           duration_ms, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.At.UTC().Format(time.RFC3339Nano), e.KeyID, e.KeyName, e.AccountID, e.AccountEmail,
		e.Model, e.Path, e.Status, e.Streaming,
		e.InputTokens, e.OutputTokens, e.CacheReadTokens, e.CacheWriteTokens,
		e.Duration.Milliseconds(), e.Error,
	)
	if err != nil {
		return fmt.Errorf("record usage: %w", err)
	}
	return nil
}

// UsageTotals is what a window of events adds up to.
type UsageTotals struct {
	Requests         int64 `json:"requests"`
	Errors           int64 `json:"errors"`
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	// P50 and P95 of wall-clock duration, in milliseconds.
	MedianMS int64 `json:"median_ms"`
	P95MS    int64 `json:"p95_ms"`
}

// UsageBucket is one group — a day, a model, a key, an account.
type UsageBucket struct {
	Label        string `json:"label"`
	ID           string `json:"id,omitempty"`
	Requests     int64  `json:"requests"`
	Errors       int64  `json:"errors"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CacheTokens  int64  `json:"cache_tokens"`
}

// UsageReport is everything the Usage tab draws.
type UsageReport struct {
	Since     time.Time     `json:"since"`
	Totals    UsageTotals   `json:"totals"`
	ByDay     []UsageBucket `json:"by_day"`
	ByModel   []UsageBucket `json:"by_model"`
	ByKey     []UsageBucket `json:"by_key"`
	ByAccount []UsageBucket `json:"by_account"`
	ByStatus  []UsageBucket `json:"by_status"`
}

// Totals aggregates only the headline figures. The overview screen wants one
// row, and running the whole breakdown report to get it made the first screen
// after sign-in the most expensive query in the process.
func (s *Store) Totals(ctx context.Context, since time.Time) (UsageTotals, error) {
	from := since.UTC().Format(time.RFC3339Nano)
	var t UsageTotals
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*),
		        COALESCE(SUM(status >= 400 OR error <> ''), 0),
		        COALESCE(SUM(input_tokens), 0),
		        COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_read_tokens), 0),
		        COALESCE(SUM(cache_write_tokens), 0)
		   FROM usage_events WHERE at >= ?`, from).
		Scan(&t.Requests, &t.Errors, &t.InputTokens, &t.OutputTokens,
			&t.CacheReadTokens, &t.CacheWriteTokens)
	if err != nil {
		return t, fmt.Errorf("usage totals: %w", err)
	}
	if t.Requests > 0 {
		t.MedianMS = s.durationPercentile(ctx, from, t.Requests, 50)
		t.P95MS = s.durationPercentile(ctx, from, t.Requests, 95)
	}
	return t, nil
}

// Usage aggregates everything since a point in time.
//
// One grouped pass over the window, folded in Go, rather than a query per
// breakdown. The five breakdowns are five different ways of adding up the same
// rows, so running five scans to produce them meant re-reading the whole window
// five times — measured at 496 ms for 7 days of moderate traffic on fast
// hardware, and 2.16 s for 30, before the cost of doing it on a single core
// with a cold page cache. The grouped form answers all five at once, 3.4x
// faster, and returns at most (days x models x keys x accounts x statuses)
// rows, which for a personal gateway is hundreds.
//
// The percentiles keep their own queries: they need the rows ordered by
// duration rather than grouped, and a rewrite to window functions measured no
// better.
func (s *Store) Usage(ctx context.Context, since time.Time) (UsageReport, error) {
	from := since.UTC().Format(time.RFC3339Nano)
	rep := UsageReport{Since: since.UTC()}

	rows, err := s.db.QueryContext(ctx,
		`SELECT substr(at, 1, 10) AS day,
		        CASE WHEN model = '' THEN '(none)' ELSE model END AS model,
		        key_id,
		        CASE WHEN key_name = '' THEN '(deleted key)' ELSE key_name END AS key_name,
		        account_id,
		        CASE WHEN account_email = '' THEN '(unknown)' ELSE account_email END AS account_email,
		        status,
		        COUNT(*),
		        COALESCE(SUM(status >= 400 OR error <> ''), 0),
		        COALESCE(SUM(input_tokens), 0),
		        COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_read_tokens), 0),
		        COALESCE(SUM(cache_write_tokens), 0)
		   FROM usage_events WHERE at >= ?
		  GROUP BY day, model, key_id, key_name, account_id, account_email, status`, from)
	if err != nil {
		return rep, fmt.Errorf("usage report: %w", err)
	}
	defer rows.Close()

	byDay := newBucketSet()
	byModel := newBucketSet()
	byKey := newBucketSet()
	byAccount := newBucketSet()
	byStatus := newBucketSet()

	for rows.Next() {
		var (
			day, model, keyID, keyName, accountID, accountEmail string
			status                                              int
			requests, errCount                                  int64
			in, out, cacheRead, cacheWrite                      int64
		)
		if err := rows.Scan(&day, &model, &keyID, &keyName, &accountID, &accountEmail,
			&status, &requests, &errCount, &in, &out, &cacheRead, &cacheWrite); err != nil {
			return rep, fmt.Errorf("usage report: %w", err)
		}
		rep.Totals.Requests += requests
		rep.Totals.Errors += errCount
		rep.Totals.InputTokens += in
		rep.Totals.OutputTokens += out
		rep.Totals.CacheReadTokens += cacheRead
		rep.Totals.CacheWriteTokens += cacheWrite

		// Buckets report cache as one figure; only the totals split it.
		g := UsageBucket{
			Requests: requests, Errors: errCount,
			InputTokens: in, OutputTokens: out,
			CacheTokens: cacheRead + cacheWrite,
		}
		byDay.add(day, "", g)
		byModel.add(model, "", g)
		byKey.add(keyName, keyID, g)
		byAccount.add(accountEmail, accountID, g)
		byStatus.add(strconv.Itoa(status), "", g)
	}
	if err := rows.Err(); err != nil {
		return rep, fmt.Errorf("usage report: %w", err)
	}

	if rep.Totals.Requests > 0 {
		// Percentiles by offset rather than by any of SQLite's missing window
		// helpers: exact, and cheap enough on a table this size.
		rep.Totals.MedianMS = s.durationPercentile(ctx, from, rep.Totals.Requests, 50)
		rep.Totals.P95MS = s.durationPercentile(ctx, from, rep.Totals.Requests, 95)
	}

	// Days oldest-first so a chart renders left to right; everything else
	// busiest-first, which is the order the old per-breakdown queries produced.
	rep.ByDay = byDay.sortedByLabel()
	rep.ByModel = byModel.sortedByRequests()
	rep.ByKey = byKey.sortedByRequests()
	rep.ByAccount = byAccount.sortedByRequests()
	rep.ByStatus = byStatus.sortedByRequests()

	// The status breakdown never carried token counts.
	for i := range rep.ByStatus {
		rep.ByStatus[i].InputTokens = 0
		rep.ByStatus[i].OutputTokens = 0
		rep.ByStatus[i].CacheTokens = 0
	}
	return rep, nil
}

// bucketSet folds grouped rows into one breakdown, keyed by label and id so
// two keys that share a name stay apart.
type bucketSet struct {
	index map[string]int
	list  []UsageBucket
}

func newBucketSet() *bucketSet { return &bucketSet{index: map[string]int{}} }

func (b *bucketSet) add(label, id string, g UsageBucket) {
	k := id + "\x00" + label
	i, ok := b.index[k]
	if !ok {
		i = len(b.list)
		b.index[k] = i
		b.list = append(b.list, UsageBucket{Label: label, ID: id})
	}
	t := &b.list[i]
	t.Requests += g.Requests
	t.Errors += g.Errors
	t.InputTokens += g.InputTokens
	t.OutputTokens += g.OutputTokens
	t.CacheTokens += g.CacheTokens
}

func (b *bucketSet) sortedByLabel() []UsageBucket {
	out := b.list
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

func (b *bucketSet) sortedByRequests() []UsageBucket {
	out := b.list
	sort.Slice(out, func(i, j int) bool {
		if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		return out[i].Label < out[j].Label
	})
	return out
}

func (s *Store) durationPercentile(ctx context.Context, from string, n int64, pct int) int64 {
	offset := (n*int64(pct))/100 - 1
	if offset < 0 {
		offset = 0
	}
	var ms int64
	err := s.db.QueryRowContext(ctx,
		`SELECT duration_ms FROM usage_events WHERE at >= ?
		  ORDER BY duration_ms ASC LIMIT 1 OFFSET ?`, from, offset).Scan(&ms)
	if err != nil {
		return 0
	}
	return ms
}

func (s *Store) usageBuckets(ctx context.Context, query, from string) ([]UsageBucket, error) {
	rows, err := s.db.QueryContext(ctx, query, from)
	if err != nil {
		return nil, fmt.Errorf("usage breakdown: %w", err)
	}
	defer rows.Close()

	out := []UsageBucket{}
	for rows.Next() {
		var b UsageBucket
		if err := rows.Scan(&b.Label, &b.ID, &b.Requests, &b.Errors,
			&b.InputTokens, &b.OutputTokens, &b.CacheTokens); err != nil {
			return nil, fmt.Errorf("usage breakdown: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// RecentUsage returns the newest events first, for the activity list.
func (s *Store) RecentUsage(ctx context.Context, limit int) ([]UsageEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT at, key_id, key_name, account_id, account_email, model, path,
		        status, streaming, input_tokens, output_tokens,
		        cache_read_tokens, cache_write_tokens, duration_ms, error
		   FROM usage_events ORDER BY at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("recent usage: %w", err)
	}
	defer rows.Close()

	out := []UsageEvent{}
	for rows.Next() {
		var e UsageEvent
		var at string
		var ms int64
		if err := rows.Scan(&at, &e.KeyID, &e.KeyName, &e.AccountID, &e.AccountEmail,
			&e.Model, &e.Path, &e.Status, &e.Streaming,
			&e.InputTokens, &e.OutputTokens, &e.CacheReadTokens, &e.CacheWriteTokens,
			&ms, &e.Error); err != nil {
			return nil, fmt.Errorf("recent usage: %w", err)
		}
		e.At, _ = time.Parse(time.RFC3339Nano, at)
		e.Duration = time.Duration(ms) * time.Millisecond
		out = append(out, e)
	}
	return out, rows.Err()
}

// KeyUsage totals one key's traffic since a point in time. Used by the keys
// list, so a key that is quietly doing nothing is visible as such.
func (s *Store) KeyUsage(ctx context.Context, since time.Time) (map[string]UsageBucket, error) {
	buckets, err := s.usageBuckets(ctx,
		`SELECT key_id AS label, key_id AS id, COUNT(*),
		        COALESCE(SUM(status >= 400 OR error <> ''), 0),
		        COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_read_tokens + cache_write_tokens), 0)
		   FROM usage_events WHERE at >= ? GROUP BY key_id`,
		since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	out := make(map[string]UsageBucket, len(buckets))
	for _, b := range buckets {
		out[b.ID] = b
	}
	return out, nil
}

// AccountUsage totals each account's traffic since a point in time, keyed by
// account id.
func (s *Store) AccountUsage(ctx context.Context, since time.Time) (map[string]UsageBucket, error) {
	buckets, err := s.usageBuckets(ctx,
		`SELECT account_id AS label, account_id AS id, COUNT(*),
		        COALESCE(SUM(status >= 400 OR error <> ''), 0),
		        COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_read_tokens + cache_write_tokens), 0)
		   FROM usage_events WHERE at >= ? GROUP BY account_id`,
		since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	out := make(map[string]UsageBucket, len(buckets))
	for _, b := range buckets {
		out[b.ID] = b
	}
	return out, nil
}

// PruneUsage drops events older than the retention window and reports how many
// went. Retention is the only thing keeping this table bounded.
func (s *Store) PruneUsage(ctx context.Context, keep time.Duration) (int64, error) {
	if keep <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().Add(-keep).Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `DELETE FROM usage_events WHERE at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune usage: %w", err)
	}
	n, _ := res.RowsAffected()

	// Hand the freed pages back to the filesystem. Deleting rows only marks
	// pages reusable, so without this the file stays at its historical peak
	// for ever: a month of heavy traffic, or an operator lowering
	// retention-days to recover space, would free nothing at all.
	//
	// A no-op on a database created before auto_vacuum was set — see Vacuum —
	// and best-effort either way, because reclaiming space is not worth
	// failing a prune over.
	if n > 0 {
		if _, err := s.db.ExecContext(ctx, `PRAGMA incremental_vacuum`); err != nil {
			return n, nil
		}
	}
	return n, nil
}

// Vacuum rewrites the database, compacting it and applying auto_vacuum to a
// file that predates it.
//
// This is the one-off an existing install needs. auto_vacuum can only be set on
// an empty database, so a gateway that has been running since before it was
// configured keeps growing regardless; VACUUM is what converts it, and from
// then on the daily prune keeps the file honest by itself.
//
// It rewrites the whole file, so it wants roughly twice the database's size
// free and takes an exclusive lock for the duration. Run it when the gateway is
// idle.
func (s *Store) Vacuum(ctx context.Context) (before, after int64, err error) {
	before, _ = s.fileSize(ctx)
	if _, err := s.db.ExecContext(ctx, `VACUUM`); err != nil {
		return before, before, fmt.Errorf("vacuum: %w", err)
	}
	after, _ = s.fileSize(ctx)
	return before, after, nil
}

// fileSize is what SQLite thinks the database occupies, which is the figure
// VACUUM changes.
func (s *Store) fileSize(ctx context.Context) (int64, error) {
	var pageCount, pageSize int64
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount); err != nil {
		return 0, err
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return 0, err
	}
	return pageCount * pageSize, nil
}

// AutoVacuum reports the mode in force: 0 none, 1 full, 2 incremental. A zero
// here on a long-running install is why the file never shrinks.
func (s *Store) AutoVacuum(ctx context.Context) (int, error) {
	var mode int
	err := s.db.QueryRowContext(ctx, `PRAGMA auto_vacuum`).Scan(&mode)
	return mode, err
}
