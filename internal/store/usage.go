package store

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// UsageEvent is one proxied request, as it happened.
type UsageEvent struct {
	// ID is set on read, not on write: it is the table's own row id, and it is
	// what makes a page boundary unambiguous when two requests share a
	// timestamp.
	ID           int64
	At           time.Time
	KeyID        string
	KeyName      string
	AccountID    string
	AccountEmail string
	Model        string
	Path         string
	// ConversationID is the chat this request belonged to, as the client named
	// it, or empty when the client named none. Client is the product that made
	// it. Both are read from the caller's own headers — never derived from the
	// messages — which is why a chat can be grouped without the gateway
	// storing any conversation content.
	ConversationID   string
	Client           string
	Status           int
	Streaming        bool
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	Duration         time.Duration
	Error            string
	// ErrorCode is that failure in one word, decided once when the request was
	// recorded: the upstream's own type, "content_check" for the refusal whose
	// message is about billing and is not, "no_answer" when nothing came back,
	// or empty when the request worked.
	//
	// Stored rather than read back out of the text, because the text ends in a
	// request_id and so no two are equal — which makes it useless as a filter
	// and unusable as a menu.
	ErrorCode string
	// Rejected is true for a request the gateway refused before it reached the
	// upstream — no key, an unknown key. Those spent nothing and have no key,
	// model or account to group under, so every aggregate steps over them and
	// only the request log shows both.
	//
	// Named for the exception so its zero value is the rule: a caller that does
	// not think about this files a relayed request, which is what all but two
	// call sites are.
	Rejected bool
	// IP is which machine made the request. For a refused one it is the only
	// identity there is; for a relayed one it is the question a key and a
	// User-Agent cannot answer, which is where it ran.
	//
	// Only as true as the proxy in front makes it: without that proxy's address
	// in trusted-proxies, X-Forwarded-For is ignored and every client is
	// recorded as the proxy.
	IP string
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
		                           model, path, conversation_id, client,
		                           status, streaming,
		                           input_tokens, output_tokens,
		                           cache_read_tokens, cache_write_tokens,
		                           duration_ms, error, error_code, rejected, ip)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.At.UTC().Format(time.RFC3339Nano), e.KeyID, e.KeyName, e.AccountID, e.AccountEmail,
		e.Model, e.Path, e.ConversationID, e.Client, e.Status, e.Streaming,
		e.InputTokens, e.OutputTokens, e.CacheReadTokens, e.CacheWriteTokens,
		e.Duration.Milliseconds(), e.Error, e.ErrorCode, e.Rejected, e.IP,
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
	from := since.UTC().Format(time.RFC3339Nano)
	var t UsageTotals
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*),
		        COALESCE(SUM(status >= 400 OR error <> ''), 0),
		        COALESCE(SUM(input_tokens), 0),
		        COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_read_tokens), 0),
		        COALESCE(SUM(cache_write_tokens), 0)
		   FROM usage_events WHERE rejected = 0 AND at >= ?`, from).
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
		   FROM usage_events WHERE rejected = 0 AND at >= ?
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
		byDay.add(day, "", g)
		byModel.add(model, "", g)
		byKey.add(keyName, keyID, g)
		byAccount.add(accountEmail, accountID, g)
		byStatus.add(strconv.Itoa(status), "", g)

		cross.add(CrossModel, day, model, g)
		// Folded by name rather than by id, because two keys minted, used and
		// deleted under one name are two entities but one line on a chart, and
		// a chart with three lines all labelled "surrtest" says nothing.
		cross.add(CrossKey, day, keyName, g)
		cross.add(CrossAccount, day, accountEmail, g)
		cross.add(CrossStatus, day, strconv.Itoa(status), g)
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

	rep.Cross = cross.sorted()
	// The status cross-tab never carried token counts either; see below.
	for i := range rep.Cross[CrossStatus] {
		rep.Cross[CrossStatus][i].InputTokens = 0
		rep.Cross[CrossStatus][i].OutputTokens = 0
		rep.Cross[CrossStatus][i].CacheTokens = 0
	}

	// The status breakdown never carried token counts.
	for i := range rep.ByStatus {
		rep.ByStatus[i].InputTokens = 0
		rep.ByStatus[i].OutputTokens = 0
		rep.ByStatus[i].CacheTokens = 0
	}
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

func (s *Store) durationPercentile(ctx context.Context, from string, n int64, pct int) int64 {
	offset := (n*int64(pct))/100 - 1
	if offset < 0 {
		offset = 0
	}
	var ms int64
	err := s.db.QueryRowContext(ctx,
		`SELECT duration_ms FROM usage_events WHERE rejected = 0 AND at >= ?
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

// UsageCursor marks a position in the activity list.
//
// A position rather than an offset. OFFSET counts from the top of a list that
// is growing at the top, so between one page and the next every row shifts down
// and paging re-shows rows it has already shown. Naming the last row seen
// instead means a page is the rows after *that row*, whatever has arrived
// since — which is the behaviour anyone scrolling a live list expects.
//
// The pair is needed, not just the timestamp: `at` is when a request started,
// so two requests that started inside the same nanosecond, or a long stream
// recorded out of order, would otherwise make a page boundary ambiguous and
// silently drop a row.
type UsageCursor struct {
	At time.Time
	ID int64
}

// String renders a cursor for a URL. Opaque to the client, legible in a log.
func (c UsageCursor) String() string {
	if c.ID == 0 {
		return ""
	}
	return c.At.UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(c.ID, 10)
}

// ParseUsageCursor reads one back. An unparseable cursor is not an error: it
// means "start from the top", which is the only useful thing to do with a
// bookmark from an older release or a truncated URL.
func ParseUsageCursor(s string) (UsageCursor, bool) {
	at, id, found := strings.Cut(s, "|")
	if !found {
		return UsageCursor{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return UsageCursor{}, false
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return UsageCursor{}, false
	}
	return UsageCursor{At: t, ID: n}, true
}

// RequestFilter narrows the request log.
//
// Every field is optional and they compose: a chat and a failure state
// together answer "which requests of this conversation broke", which is the
// question a count in the chat table raises and could not answer.
//
// The set-valued fields hold every value the caller kept rather than one,
// because a column menu is a column of checkboxes and "sonnet or haiku" is a
// question a single value cannot ask. One tick is a one-element set, so
// nothing here special-cases the common case.
//
// An empty string in a set is a real choice, not an absent one: a refused
// request has no model and the gateway's own titling rows carry no client, and
// "the ones with nothing here" is a thing worth asking for. So a set that does
// not narrow is nil, never present-and-empty.
//
// Named for the log rather than for usage, because they are different things.
// Usage is what was spent; the log is what arrived — including requests that
// spent nothing because they were refused.
type RequestFilter struct {
	// ConversationIDs limits to these chats, KeyIDs to these credentials,
	// Models to these models, Clients to what was running, IPs to these
	// machines, Statuses to these response codes.
	ConversationIDs []string
	KeyIDs          []string
	Models          []string
	Clients         []string
	IPs             []string
	Statuses        []int
	// ErrorCodes limits to these outcomes: the class each request was given
	// when it was recorded, which is the only thing on the row the message
	// column can be filtered by. The text itself ends in a request_id, so no
	// two are equal.
	ErrorCodes []string
	// Kinds narrows to "relayed", "rejected", or both — which is the same as
	// neither, and is the point of one list.
	//
	// A set like the others, because it is a column now rather than a control
	// above the table, and a column's menu is a column of checkboxes.
	Kinds []string
	// FailedOnly keeps what did not work: a status the caller would call a
	// failure, or a stream that died after its 200. The two are different
	// facts and both are failures, which is why this is one flag rather than
	// a status-code field the caller has to know to combine.
	//
	// Not the same question as a Statuses set, and it composes with one: 429
	// and 500 are two codes, "anything that went wrong" is a predicate over
	// codes this gateway did not choose.
	FailedOnly bool
}

// where builds the clause and its arguments, without the leading keyword.
func (f RequestFilter) where() (string, []any) {
	var clauses []string
	var args []any

	set := func(column string, values []string) {
		if len(values) == 0 {
			return
		}
		clauses = append(clauses, column+" IN ("+placeholders(len(values))+")")
		for _, v := range values {
			args = append(args, v)
		}
	}
	set("conversation_id", f.ConversationIDs)
	set("key_id", f.KeyIDs)
	set("model", f.Models)
	set("client", f.Clients)
	set("ip", f.IPs)
	set("error_code", f.ErrorCodes)

	if len(f.Statuses) > 0 {
		clauses = append(clauses, "status IN ("+placeholders(len(f.Statuses))+")")
		for _, v := range f.Statuses {
			args = append(args, v)
		}
	}

	// Both ticked is every row, so it narrows nothing and says nothing — the
	// same as neither, which is what an untouched menu means.
	relayed, rejected := contains(f.Kinds, "relayed"), contains(f.Kinds, "rejected")
	switch {
	case relayed && !rejected:
		clauses = append(clauses, "rejected = 0")
	case rejected && !relayed:
		clauses = append(clauses, "rejected = 1")
	}
	if f.FailedOnly {
		clauses = append(clauses, "(status = 0 OR status >= 400 OR error <> '')")
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return strings.Join(clauses, " AND "), args
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// placeholders is "?, ?, ?" for three, so a set of values becomes an IN list.
//
// Built from the count and never from the values themselves, which is the
// whole point: the values stay parameters, and a model name with a quote in it
// is a string rather than a syntax error or worse.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// FacetValue is one line of a column's filter menu: what to narrow to, what to
// show for it, and how many requests carry it.
type FacetValue struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
	Count int64  `json:"count"`
}

// RequestFacets is what each filterable column can be narrowed to.
type RequestFacets struct {
	Models   []FacetValue `json:"models"`
	Clients  []FacetValue `json:"clients"`
	IPs      []FacetValue `json:"ips"`
	Statuses []FacetValue `json:"statuses"`
	Chats    []FacetValue `json:"chats"`
	Messages []FacetValue `json:"messages"`
	Kinds    []FacetValue `json:"kinds"`
}

// RequestFacets lists what every filterable column holds, with counts.
//
// Read from the whole table rather than from the page on screen. The screen
// holds fifty rows, which is about seven minutes of a busy gateway, and a menu
// built from those can only ever offer what you are already looking at — the
// exact defect that made the old model picker useless. The log searches all of
// history and so does this.
//
// Deliberately not narrowed by the filter in force either. Counts that respond
// to the other filters look clever and take away the only way back: tick a
// model, and every value that model never used disappears from the other
// menus, so the filter that trapped you is the one you can no longer widen.
// These are totals, and the UI says so.
//
// Capped per column, commonest first. Addresses are the unbounded one — a
// scanner can mint thousands in an afternoon — and a menu is not a place to
// render them all; what a cap loses is the long tail of one-request values,
// which the IP cell is clickable for.
func (s *Store) RequestFacets(ctx context.Context, limit int) (RequestFacets, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	var out RequestFacets
	var err error
	if out.Models, err = s.facet(ctx, "model", limit); err != nil {
		return RequestFacets{}, err
	}
	if out.Clients, err = s.facet(ctx, "client", limit); err != nil {
		return RequestFacets{}, err
	}
	if out.IPs, err = s.facet(ctx, "ip", limit); err != nil {
		return RequestFacets{}, err
	}
	if out.Statuses, err = s.statusFacet(ctx, limit); err != nil {
		return RequestFacets{}, err
	}
	if out.Chats, err = s.chatFacet(ctx, limit); err != nil {
		return RequestFacets{}, err
	}
	if out.Messages, err = s.facet(ctx, "error_code", limit); err != nil {
		return RequestFacets{}, err
	}
	if out.Kinds, err = s.kindFacet(ctx); err != nil {
		return RequestFacets{}, err
	}
	return out, nil
}

// facet counts one column whose value is also its label.
//
// The column name is a constant from the caller, never anything a request
// carries: it is the one part of the statement that cannot be a parameter.
func (s *Store) facet(ctx context.Context, column string, limit int) ([]FacetValue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+column+`, COUNT(*) FROM usage_events
		GROUP BY `+column+`
		ORDER BY COUNT(*) DESC, `+column+` ASC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("facet %s: %w", column, err)
	}
	defer rows.Close()

	out := []FacetValue{}
	for rows.Next() {
		var v FacetValue
		if err := rows.Scan(&v.Value, &v.Count); err != nil {
			return nil, fmt.Errorf("facet %s: %w", column, err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// kindFacet counts what did and did not reach the upstream.
//
// Two rows at most, and it speaks the filter's vocabulary rather than the
// column's storage: the table holds a boolean, the filter says "relayed" or
// "rejected", and translating here keeps that word in one place.
func (s *Store) kindFacet(ctx context.Context) ([]FacetValue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT rejected, COUNT(*) FROM usage_events GROUP BY rejected ORDER BY COUNT(*) DESC`)
	if err != nil {
		return nil, fmt.Errorf("facet kinds: %w", err)
	}
	defer rows.Close()

	out := []FacetValue{}
	for rows.Next() {
		var rejected bool
		var count int64
		if err := rows.Scan(&rejected, &count); err != nil {
			return nil, fmt.Errorf("facet kinds: %w", err)
		}
		// The column is called Forwarded and answers yes or no, so the menu
		// does too. The stored values keep their own names — "relayed" and
		// "rejected" are what the filter and the URL have always said, and a
		// link written last week still works.
		v := FacetValue{Value: "relayed", Label: "yes", Count: count}
		if rejected {
			v = FacetValue{Value: "rejected", Label: "no", Count: count}
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// chatFacet counts by conversation, labelled with the name the chat has if it
// has one.
//
// Its own query rather than facet("conversation_id") because an id is not
// something anyone recognises: a menu of sixteen hex characters is a menu you
// cannot use. The title comes from the same table the Chats screen reads, and
// a conversation without one keeps its id as the label.
//
// The empty conversation is excluded. Every request whose client named no chat
// shares it, which on most gateways is the largest bucket and is not a chat.
func (s *Store) chatFacet(ctx context.Context, limit int) ([]FacetValue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.conversation_id, COALESCE(t.title, ''), COUNT(*)
		FROM usage_events e
		LEFT JOIN chat_titles t ON t.conversation_id = e.conversation_id
		WHERE e.conversation_id <> ''
		GROUP BY e.conversation_id
		ORDER BY COUNT(*) DESC, e.conversation_id ASC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("facet chats: %w", err)
	}
	defer rows.Close()

	out := []FacetValue{}
	for rows.Next() {
		var v FacetValue
		var title string
		if err := rows.Scan(&v.Value, &title, &v.Count); err != nil {
			return nil, fmt.Errorf("facet chats: %w", err)
		}
		v.Label = title
		out = append(out, v)
	}
	return out, rows.Err()
}

// statusFacet counts by response code, with 0 named for what it means.
func (s *Store) statusFacet(ctx context.Context, limit int) ([]FacetValue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT status, COUNT(*) FROM usage_events
		GROUP BY status
		ORDER BY COUNT(*) DESC, status ASC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("facet statuses: %w", err)
	}
	defer rows.Close()

	out := []FacetValue{}
	for rows.Next() {
		var code int
		var count int64
		if err := rows.Scan(&code, &count); err != nil {
			return nil, fmt.Errorf("facet statuses: %w", err)
		}
		// 0 is not a status any upstream sent; it is this gateway recording
		// that nothing came back at all.
		label := strconv.Itoa(code)
		if code == 0 {
			label = "no answer"
		}
		out = append(out, FacetValue{Value: strconv.Itoa(code), Label: label, Count: count})
	}
	return out, rows.Err()
}

// RecentUsage returns one page of events, newest first, along with the cursor
// for the page after it. An empty next cursor means the end of the list.
func (s *Store) RecentUsage(ctx context.Context, limit int, after UsageCursor, filter RequestFilter) ([]UsageEvent, UsageCursor, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	const columns = `id, at, key_id, key_name, account_id, account_email, model, path,
	                 conversation_id, client,
	                 status, streaming, input_tokens, output_tokens,
	                 cache_read_tokens, cache_write_tokens, duration_ms, error,
	                 error_code, rejected, ip`

	narrow, args := filter.where()
	// The cursor is a condition like any other, so it joins the same AND
	// chain. Written separately the two used to be two whole statements that
	// had to be kept in step, which is how a filter ends up applying to the
	// first page and not the rest.
	var conditions []string
	var params []any
	if after.ID > 0 {
		// Row values, so the comparison is the same one the ORDER BY makes.
		conditions = append(conditions, "(at, id) < (?, ?)")
		params = append(params, after.At.UTC().Format(time.RFC3339Nano), after.ID)
	}
	if narrow != "" {
		conditions = append(conditions, narrow)
		params = append(params, args...)
	}
	query := `SELECT ` + columns + ` FROM usage_events`
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, " AND ")
	}
	query += ` ORDER BY at DESC, id DESC LIMIT ?`
	params = append(params, limit)

	rows, err := s.db.QueryContext(ctx, query, params...)
	if err != nil {
		return nil, UsageCursor{}, fmt.Errorf("recent usage: %w", err)
	}
	defer rows.Close()

	out := []UsageEvent{}
	var next UsageCursor
	for rows.Next() {
		var e UsageEvent
		var at string
		var ms int64
		if err := rows.Scan(&e.ID, &at, &e.KeyID, &e.KeyName, &e.AccountID, &e.AccountEmail,
			&e.Model, &e.Path, &e.ConversationID, &e.Client, &e.Status, &e.Streaming,
			&e.InputTokens, &e.OutputTokens, &e.CacheReadTokens, &e.CacheWriteTokens,
			&ms, &e.Error, &e.ErrorCode, &e.Rejected, &e.IP); err != nil {
			return nil, UsageCursor{}, fmt.Errorf("recent usage: %w", err)
		}
		e.At, _ = time.Parse(time.RFC3339Nano, at)
		e.Duration = time.Duration(ms) * time.Millisecond
		out = append(out, e)
		next = UsageCursor{At: e.At, ID: e.ID}
	}
	if err := rows.Err(); err != nil {
		return nil, UsageCursor{}, fmt.Errorf("recent usage: %w", err)
	}
	// A short page is the last page. Saying so lets the caller stop offering
	// "load more" rather than finding out by fetching nothing.
	if len(out) < limit {
		next = UsageCursor{}
	}
	return out, next, nil
}

// KeyUsage totals one key's traffic since a point in time. Used by the keys
// list, so a key that is quietly doing nothing is visible as such.
func (s *Store) KeyUsage(ctx context.Context, since time.Time) (map[string]UsageBucket, error) {
	buckets, err := s.usageBuckets(ctx,
		`SELECT key_id AS label, key_id AS id, COUNT(*),
		        COALESCE(SUM(status >= 400 OR error <> ''), 0),
		        COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_read_tokens + cache_write_tokens), 0)
		   FROM usage_events WHERE rejected = 0 AND at >= ? GROUP BY key_id`,
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

// KeySpend totals what one key has spent since a point in time.
//
// The same four columns UsageEvent.Tokens adds up, because all four are
// billed — a budget that ignored cache reads would let a key with a large
// cached prefix spend most of a subscription for free, on paper.
//
// Bounded by time rather than by key, which is what makes it cheap enough to
// sit on the request path: idx_usage_at narrows to the window first, and a
// budget window holds a small slice of the table.
func (s *Store) KeySpend(ctx context.Context, keyID string, since time.Time) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(input_tokens + output_tokens
		                    + cache_read_tokens + cache_write_tokens), 0)
		   FROM usage_events WHERE rejected = 0 AND at >= ? AND key_id = ?`,
		since.UTC().Format(time.RFC3339Nano), keyID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("key spend: %w", err)
	}
	return n, nil
}

// OldestSpendAt is when the earliest still-counted request for a key happened,
// so a refusal can say when the budget starts freeing up rather than telling
// the client to guess.
func (s *Store) OldestSpendAt(ctx context.Context, keyID string, since time.Time) (time.Time, bool) {
	var at string
	err := s.db.QueryRowContext(ctx,
		`SELECT MIN(at) FROM usage_events
		  WHERE rejected = 0 AND at >= ? AND key_id = ?
		    AND input_tokens + output_tokens + cache_read_tokens + cache_write_tokens > 0`,
		since.UTC().Format(time.RFC3339Nano), keyID).Scan(&at)
	if err != nil || at == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// AccountUsage totals each account's traffic since a point in time, keyed by
// account id.
func (s *Store) AccountUsage(ctx context.Context, since time.Time) (map[string]UsageBucket, error) {
	buckets, err := s.usageBuckets(ctx,
		`SELECT account_id AS label, account_id AS id, COUNT(*),
		        COALESCE(SUM(status >= 400 OR error <> ''), 0),
		        COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_read_tokens + cache_write_tokens), 0)
		   FROM usage_events WHERE rejected = 0 AND at >= ? GROUP BY account_id`,
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

// Snapshot writes a consistent copy of the database to path, while the gateway
// keeps serving.
//
// `cp` is not a backup here. The database runs in WAL mode, so at any moment
// committed data lives partly in claudication.db and partly in the -wal file;
// copying either alone, or both without synchronisation, can produce a file
// that is torn or silently missing the most recent accounts and keys. VACUUM
// INTO takes a read transaction and writes a whole, self-contained database —
// already compacted, and with no -wal beside it to remember to bring along.
//
// The target must not exist: SQLite refuses to overwrite, and that refusal is
// worth keeping rather than working around.
func (s *Store) Snapshot(ctx context.Context, path string) error {
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("snapshot database: %w", err)
	}
	return nil
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
