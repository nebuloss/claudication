package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// UsageCursor marks a position in the activity list.
//
// A position rather than an offset. OFFSET counts from the top of a list that
// is growing at the top, so between one page and the next every row shifts down
// and paging re-shows rows it has already shown. Naming the last row seen
// instead means a page is the rows after *that row*, whatever has arrived
// since — which is the behaviour anyone scrolling a live list expects.
//
// The pair is needed, not just the timestamp: `at` is when a request started,
// so two requests that started inside the same millisecond, or a long stream
// recorded out of order, would otherwise make a page boundary ambiguous and
// silently drop a row.
type UsageCursor struct {
	At time.Time
	ID int64
}

// String renders a cursor for a URL: the row's unix ms and its id. Opaque to
// the client, legible in a log.
func (c UsageCursor) String() string {
	if c.ID == 0 {
		return ""
	}
	return strconv.FormatInt(ms(c.At), 10) + "|" + strconv.FormatInt(c.ID, 10)
}

// ParseUsageCursor reads one back: the unix-ms form String writes, or the
// text time a release before integer times wrote, so a page open across the
// upgrade carries on. An unparseable cursor is not an error: it means "start
// from the top", which is the only useful thing to do with a truncated URL.
func ParseUsageCursor(s string) (UsageCursor, bool) {
	at, id, found := strings.Cut(s, "|")
	if !found {
		return UsageCursor{}, false
	}
	var t time.Time
	if n, err := strconv.ParseInt(at, 10, 64); err == nil {
		t = fromMS(n)
	} else if old, err := time.Parse(time.RFC3339Nano, at); err == nil {
		t = old
	} else {
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
	// AccountIDs limits to the subscription accounts that served them.
	AccountIDs []string
	Models     []string
	Clients    []string
	IPs        []string
	Statuses   []int
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
	// Outcome keeps what did, or did not, work: "failed" is a status the
	// caller would call a failure or a stream that died after its 200, "ok" is
	// the rest, and empty is both.
	//
	// A predicate rather than a set, and not the same question as Statuses: 429
	// and 500 are two codes, "anything that went wrong" is a statement about
	// codes this gateway did not choose plus a failure mode that has no code
	// at all. The two compose.
	Outcome string
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
	set("account_id", f.AccountIDs)
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
	// "relayed" is everything that reached the upstream, the gateway's own
	// titling included. kind = 1 is spelled as the partial index
	// idx_usage_rejected is, which is what lets the planner use it.
	switch {
	case relayed && !rejected:
		clauses = append(clauses, "kind <> 1")
	case rejected && !relayed:
		clauses = append(clauses, "kind = 1")
	}
	switch f.Outcome {
	case "failed":
		clauses = append(clauses, sqlIsError)
	case "ok":
		// The exact complement of the line above, so the two partition the
		// table and a request cannot be neither.
		clauses = append(clauses, sqlIsOK)
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

// MaxRequestsPage is the most events RecentUsage returns at once. Larger asks
// get this many rather than the default, so a caller that clamps to it and the
// store agree on what a page is.
const MaxRequestsPage = 1000

// RecentUsage returns one page of events, newest first, along with the cursor
// for the page after it. An empty next cursor means the end of the list.
func (s *Store) RecentUsage(ctx context.Context, limit int, after UsageCursor, filter RequestFilter) ([]UsageEvent, UsageCursor, error) {
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, MaxRequestsPage)

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
		params = append(params, ms(after.At), after.ID)
	}
	if narrow != "" {
		conditions = append(conditions, narrow)
		params = append(params, args...)
	}
	query := `SELECT ` + usageEventColumns + ` FROM usage_events`
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
		e, err := scanUsageEvent(rows)
		if err != nil {
			return nil, UsageCursor{}, fmt.Errorf("recent usage: %w", err)
		}
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
