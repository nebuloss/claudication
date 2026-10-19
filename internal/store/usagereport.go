package store

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"
)

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

// UsageCell is one name on one day — a model, a key, an account, a status.
//
// Cross-tabs cost nothing to produce. The report already groups by day and by
// every one of those in a single pass; it simply threw the pairings away and
// kept the margins. This is that pass folded once more rather than a sixth
// query.
type UsageCell struct {
	Day          string `json:"day"`
	Name         string `json:"name"`
	Requests     int64  `json:"requests"`
	Errors       int64  `json:"errors"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CacheTokens  int64  `json:"cache_tokens"`
}

// The dimensions a cross-tab is offered for. Names rather than an enum because
// they cross the wire and index the chart's tabs on the other side.
const (
	CrossModel   = "model"
	CrossKey     = "key"
	CrossAccount = "account"
	CrossStatus  = "status"
)

// UsageReport is everything the Usage tab draws.
type UsageReport struct {
	Since     time.Time     `json:"since"`
	Totals    UsageTotals   `json:"totals"`
	ByDay     []UsageBucket `json:"by_day"`
	ByModel   []UsageBucket `json:"by_model"`
	ByKey     []UsageBucket `json:"by_key"`
	ByAccount []UsageBucket `json:"by_account"`
	ByStatus  []UsageBucket `json:"by_status"`
	// Cross holds each breakdown crossed with the day, oldest first, keyed by
	// the Cross* names above. Bounded by days x names per dimension, which for
	// a personal gateway is a few hundred rows in total.
	Cross map[string][]UsageCell `json:"cross"`
}

// Totals aggregates only the headline figures. The overview screen wants one
// row, and running the whole breakdown report to get it made the first screen
// after sign-in the most expensive query in the process.
func (s *Store) Totals(ctx context.Context, since time.Time) (UsageTotals, error) {
	from := ms(since)
	var t UsageTotals
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*),
		        COALESCE(SUM(`+sqlIsError+`), 0),
		        COALESCE(SUM(input_tokens), 0),
		        COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_read_tokens), 0),
		        COALESCE(SUM(cache_write_tokens), 0)
		   FROM usage_events WHERE `+sqlCounted+` AND at >= ?`, from).
		Scan(&t.Requests, &t.Errors, &t.InputTokens, &t.OutputTokens,
			&t.CacheReadTokens, &t.CacheWriteTokens)
	if err != nil {
		return t, fmt.Errorf("usage totals: %w", err)
	}
	if t.Requests > 0 {
		t.MedianMS = s.durationPercentile(ctx, from, UsageScope{}, t.Requests, 50)
		t.P95MS = s.durationPercentile(ctx, from, UsageScope{}, t.Requests, 95)
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
	return s.UsageIn(ctx, since, UsageScope{})
}

// UsageScope narrows a report to one account, one key, or both. The zero
// value is the whole gateway.
type UsageScope struct {
	AccountID string
	KeyID     string
}

// where is the scope as extra conditions on usage_events, each led by AND so
// it appends to a WHERE that already has one.
func (sc UsageScope) where() (string, []any) {
	var clause string
	var args []any
	if sc.AccountID != "" {
		clause += " AND account_id = ?"
		args = append(args, sc.AccountID)
	}
	if sc.KeyID != "" {
		clause += " AND key_id = ?"
		args = append(args, sc.KeyID)
	}
	return clause, args
}

// UsageIn is Usage over one account's or one key's requests: the same report,
// so a detail page and the gateway-wide view read alike.
func (s *Store) UsageIn(ctx context.Context, since time.Time, scope UsageScope) (UsageReport, error) {
	from := ms(since)
	rep := UsageReport{Since: since.UTC()}
	narrow, narrowArgs := scope.where()

	// Grouped by id, then named: one names lookup per group rather than per
	// row. A day is UTC, as it was when it was the first ten characters of a
	// text time.
	rows, err := s.db.QueryContext(ctx,
		`SELECT day, model, key_id,
		        COALESCE(NULLIF(`+nameOf(nameKey, "g.key_id")+`, ''), '(deleted key)'),
		        account_id,
		        COALESCE(NULLIF(`+nameOf(nameAccount, "g.account_id")+`, ''), '(unknown)'),
		        status, requests, errors, input, output, cache_read, cache_write
		   FROM (SELECT strftime('%Y-%m-%d', at / 1000, 'unixepoch') AS day,
		                CASE WHEN model = '' THEN '(none)' ELSE model END AS model,
		                key_id, account_id, status,
		                COUNT(*) AS requests,
		                COALESCE(SUM(`+sqlIsError+`), 0) AS errors,
		                COALESCE(SUM(input_tokens), 0) AS input,
		                COALESCE(SUM(output_tokens), 0) AS output,
		                COALESCE(SUM(cache_read_tokens), 0) AS cache_read,
		                COALESCE(SUM(cache_write_tokens), 0) AS cache_write
		           FROM usage_events WHERE `+sqlCounted+` AND at >= ?`+narrow+`
		          GROUP BY day, model, key_id, account_id, status) g`,
		append([]any{from}, narrowArgs...)...)
	if err != nil {
		return rep, fmt.Errorf("usage report: %w", err)
	}
	defer rows.Close()

	byDay := newBucketSet()
	byModel := newBucketSet()
	byKey := newBucketSet()
	byAccount := newBucketSet()
	byStatus := newBucketSet()
	cross := newCrossSet()

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
		// The status breakdown never carried token counts: a status is an
		// outcome, and the tokens are on the other breakdowns already.
		counts := UsageBucket{Requests: requests, Errors: errCount}
		byDay.add(day, "", g)
		byModel.add(model, "", g)
		byKey.add(keyName, keyID, g)
		byAccount.add(accountEmail, accountID, g)
		byStatus.add(strconv.Itoa(status), "", counts)

		cross.add(CrossModel, day, model, g)
		// Folded by name rather than by id, because two keys minted, used and
		// deleted under one name are two entities but one line on a chart, and
		// a chart with three lines all labelled "surrtest" says nothing.
		cross.add(CrossKey, day, keyName, g)
		cross.add(CrossAccount, day, accountEmail, g)
		cross.add(CrossStatus, day, strconv.Itoa(status), counts)
	}
	if err := rows.Err(); err != nil {
		return rep, fmt.Errorf("usage report: %w", err)
	}

	if rep.Totals.Requests > 0 {
		// Percentiles by offset rather than by any of SQLite's missing window
		// helpers: exact, and cheap enough on a table this size.
		rep.Totals.MedianMS = s.durationPercentile(ctx, from, scope, rep.Totals.Requests, 50)
		rep.Totals.P95MS = s.durationPercentile(ctx, from, scope, rep.Totals.Requests, 95)
	}

	// Days oldest-first so a chart renders left to right; everything else
	// busiest-first, which is the order the old per-breakdown queries produced.
	rep.ByDay = byDay.sortedByLabel()
	rep.ByModel = byModel.sortedByRequests()
	rep.ByKey = byKey.sortedByRequests()
	rep.ByAccount = byAccount.sortedByRequests()
	rep.ByStatus = byStatus.sortedByRequests()

	rep.Cross = cross.sorted()
	return rep, nil
}

// crossSet folds grouped rows into one cell per (dimension, day, name).
type crossSet map[string]map[[2]string]*UsageCell

func newCrossSet() crossSet { return crossSet{} }

func (c crossSet) add(dimension, day, name string, g UsageBucket) {
	byPair, ok := c[dimension]
	if !ok {
		byPair = map[[2]string]*UsageCell{}
		c[dimension] = byPair
	}
	// Keyed by the pair, because neither half identifies a cell on its own.
	key := [2]string{day, name}
	cell, ok := byPair[key]
	if !ok {
		cell = &UsageCell{Day: day, Name: name}
		byPair[key] = cell
	}
	cell.Requests += g.Requests
	cell.Errors += g.Errors
	cell.InputTokens += g.InputTokens
	cell.OutputTokens += g.OutputTokens
	cell.CacheTokens += g.CacheTokens
}

// sorted returns each dimension oldest day first, then by name, so the order
// is stable across reports and a chart does not reshuffle between refreshes.
func (c crossSet) sorted() map[string][]UsageCell {
	out := make(map[string][]UsageCell, len(c))
	for dimension, byPair := range c {
		cells := make([]UsageCell, 0, len(byPair))
		for _, cell := range byPair {
			cells = append(cells, *cell)
		}
		sort.Slice(cells, func(i, j int) bool {
			if cells[i].Day != cells[j].Day {
				return cells[i].Day < cells[j].Day
			}
			return cells[i].Name < cells[j].Name
		})
		out[dimension] = cells
	}
	return out
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

func (s *Store) durationPercentile(ctx context.Context, from int64, scope UsageScope, n int64, pct int) int64 {
	offset := (n*int64(pct))/100 - 1
	if offset < 0 {
		offset = 0
	}
	narrow, narrowArgs := scope.where()
	args := append(append([]any{from}, narrowArgs...), offset)
	var ms int64
	err := s.db.QueryRowContext(ctx,
		`SELECT duration_ms FROM usage_events WHERE `+sqlCounted+` AND at >= ?`+narrow+`
		  ORDER BY duration_ms ASC LIMIT 1 OFFSET ?`, args...).Scan(&ms)
	if err != nil {
		return 0
	}
	return ms
}
