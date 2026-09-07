package store

import (
	"context"
	"fmt"
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

// Usage aggregates everything since a point in time.
func (s *Store) Usage(ctx context.Context, since time.Time) (UsageReport, error) {
	from := since.UTC().Format(time.RFC3339Nano)
	rep := UsageReport{Since: since.UTC()}

	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*),
		        COALESCE(SUM(status >= 400 OR error <> ''), 0),
		        COALESCE(SUM(input_tokens), 0),
		        COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_read_tokens), 0),
		        COALESCE(SUM(cache_write_tokens), 0)
		   FROM usage_events WHERE at >= ?`, from).
		Scan(&rep.Totals.Requests, &rep.Totals.Errors,
			&rep.Totals.InputTokens, &rep.Totals.OutputTokens,
			&rep.Totals.CacheReadTokens, &rep.Totals.CacheWriteTokens)
	if err != nil {
		return rep, fmt.Errorf("usage totals: %w", err)
	}

	// Percentiles by offset rather than by any of SQLite's missing window
	// helpers: exact, and cheap enough on a table this size.
	if rep.Totals.Requests > 0 {
		rep.Totals.MedianMS = s.durationPercentile(ctx, from, rep.Totals.Requests, 50)
		rep.Totals.P95MS = s.durationPercentile(ctx, from, rep.Totals.Requests, 95)
	}

	// Days come back oldest-first so a chart can render them left to right.
	var err2 error
	rep.ByDay, err2 = s.usageBuckets(ctx,
		`SELECT substr(at, 1, 10) AS label, '' AS id, COUNT(*),
		        COALESCE(SUM(status >= 400 OR error <> ''), 0),
		        COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_read_tokens + cache_write_tokens), 0)
		   FROM usage_events WHERE at >= ? GROUP BY label ORDER BY label ASC`, from)
	if err2 != nil {
		return rep, err2
	}

	rep.ByModel, err2 = s.usageBuckets(ctx,
		`SELECT CASE WHEN model = '' THEN '(none)' ELSE model END AS label, '' AS id, COUNT(*),
		        COALESCE(SUM(status >= 400 OR error <> ''), 0),
		        COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_read_tokens + cache_write_tokens), 0)
		   FROM usage_events WHERE at >= ? GROUP BY label ORDER BY 3 DESC`, from)
	if err2 != nil {
		return rep, err2
	}

	rep.ByKey, err2 = s.usageBuckets(ctx,
		`SELECT CASE WHEN key_name = '' THEN '(deleted key)' ELSE key_name END AS label,
		        key_id AS id, COUNT(*),
		        COALESCE(SUM(status >= 400 OR error <> ''), 0),
		        COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_read_tokens + cache_write_tokens), 0)
		   FROM usage_events WHERE at >= ? GROUP BY key_id, label ORDER BY 3 DESC`, from)
	if err2 != nil {
		return rep, err2
	}

	rep.ByAccount, err2 = s.usageBuckets(ctx,
		`SELECT CASE WHEN account_email = '' THEN '(unknown)' ELSE account_email END AS label,
		        account_id AS id, COUNT(*),
		        COALESCE(SUM(status >= 400 OR error <> ''), 0),
		        COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_read_tokens + cache_write_tokens), 0)
		   FROM usage_events WHERE at >= ? GROUP BY account_id, label ORDER BY 3 DESC`, from)
	if err2 != nil {
		return rep, err2
	}

	rep.ByStatus, err2 = s.usageBuckets(ctx,
		`SELECT CAST(status AS TEXT) AS label, '' AS id, COUNT(*),
		        COALESCE(SUM(status >= 400 OR error <> ''), 0),
		        0, 0, 0
		   FROM usage_events WHERE at >= ? GROUP BY status ORDER BY 3 DESC`, from)
	return rep, err2
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
	return n, nil
}
