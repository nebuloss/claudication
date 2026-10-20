package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// UsageEvent is one proxied request, as it happened.
type UsageEvent struct {
	// ID is set on read, not on write: it is the table's own row id, and it is
	// what makes a page boundary unambiguous when two requests share a
	// timestamp.
	ID    int64
	At    time.Time
	KeyID string
	// KeyName and AccountEmail are read back from the names table: what the
	// key and the account are called now, not when the request was made. On
	// write they are ignored; a name is recorded where it is set (names.go).
	KeyName      string
	AccountID    string
	AccountEmail string
	// Provider is whose account served the request: anthropic, openai. Kept
	// on the row, like the names, so it outlives the account.
	Provider string
	Model    string
	// VirtualModel is the client's slot name it asked for, when it asked by
	// one; Model is then the real model that served it. The column is alias,
	// from when these names were gateway-wide.
	VirtualModel string
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
	// ReasoningTokens is the part of OutputTokens the model spent reasoning,
	// where the provider says. Already counted in OutputTokens.
	ReasoningTokens int
	Duration        time.Duration
	// FirstToken is how long a streamed request waited for its first token,
	// or zero when that was not measured.
	FirstToken time.Duration
	Error      string
	// ErrorCode is that failure in one word, decided once when the request was
	// recorded: the upstream's own type, "content_check" for the refusal whose
	// message is about billing and is not, "no_answer" when nothing came back,
	// "other" for a failure nothing named, or empty when the request worked.
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
	// Naming is a client's own request to name its chat (crush's and
	// opencode's, internal/service/titles): part of the chat's cost, as the
	// gateway's own titling is, and not one of its turns. On write it files
	// the row as the gateway's titling is filed, kind 2, which every
	// aggregate counts and the chat rollups' turn counts do not.
	Naming bool
}

// Tokens is the billable total: cache reads are counted because they are
// charged, at a discount the gateway has no way to know.
func (e UsageEvent) Tokens() int64 {
	return int64(e.InputTokens) + int64(e.OutputTokens) +
		int64(e.CacheReadTokens) + int64(e.CacheWriteTokens)
}

// The SQL for what a usage row means, written once. Every report, breakdown and
// filter reads these rather than spelling the expression out, because spelled
// out they drifted: the reports counted an error as status >= 400 or an error
// text, while the request log's "failed" filter also counted status 0, so the
// Overview's error count and the log filtered to failures disagreed for the
// same window.
const (
	// sqlIsError is a request that failed: a status the caller would call a
	// failure, no answer at all (status 0), or a stream that died after its 200
	// (an error text with a 2xx status). Every such row has an error_code,
	// because RecordUsage gives one to any failure the classifier did not
	// name (failed), so the class column alone says it.
	sqlIsError = `(error_code <> '')`
	// sqlIsOK is its exact complement, so the two partition the table and a
	// request cannot be neither.
	sqlIsOK = `(error_code = '')`
	// sqlCounted is a row the aggregates add up: everything that reached the
	// upstream, the gateway's own titling included, and not what it refused.
	sqlCounted = `kind <> 1`
	// sqlTokens is UsageEvent.Tokens in SQL: every billed kind, cache reads
	// included.
	sqlTokens = `(input_tokens + output_tokens + cache_read_tokens + cache_write_tokens)`
	// sqlCacheTokens is the one cache figure the breakdowns carry.
	sqlCacheTokens = `(cache_read_tokens + cache_write_tokens)`
)

// The kinds of row, as usage_events.kind stores them.
const (
	kindRelayed  = 0
	kindRejected = 1
	kindInternal = 2
)

// kind is which of those an event is. Refused first: a refusal of the
// gateway's own request is still a refusal.
func (e UsageEvent) kind() int {
	switch {
	case e.Rejected:
		return kindRejected
	case e.Path == InternalPath, e.Naming:
		return kindInternal
	}
	return kindRelayed
}

// failed is sqlIsError's definition in Go, for the one place a row is written.
func (e UsageEvent) failed() bool {
	return e.Status == 0 || e.Status >= 400 || e.Error != ""
}

// RecordUsage appends one event.
//
// It never returns an error to the request path — see the caller. Recording is
// bookkeeping, and a full disk should degrade the statistics, not the proxy.
//
// A failure the classifier left unnamed — it reads the error text, and a 500
// with no body has none — is filed as no_answer or other, so error_code is
// set exactly when the request failed and sqlIsError can be that one column.
func (s *Store) RecordUsage(ctx context.Context, e UsageEvent) error {
	if e.ErrorCode == "" && e.failed() {
		e.ErrorCode = "other"
		if e.Status == 0 {
			e.ErrorCode = "no_answer"
		}
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO usage_events (at, kind, key_id, account_id, provider, model, alias, path,
		                           conversation_id, client, ip, status, streaming,
		                           input_tokens, output_tokens,
		                           cache_read_tokens, cache_write_tokens, reasoning_tokens,
		                           duration_ms, first_token_ms, error_code, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ms(e.At), e.kind(), e.KeyID, e.AccountID, e.Provider, e.Model, e.VirtualModel, e.Path,
		e.ConversationID, e.Client, e.IP, e.Status, e.Streaming,
		e.InputTokens, e.OutputTokens, e.CacheReadTokens, e.CacheWriteTokens, e.ReasoningTokens,
		e.Duration.Milliseconds(), e.FirstToken.Milliseconds(), e.ErrorCode, e.Error,
	)
	if err != nil {
		return fmt.Errorf("record usage: %w", err)
	}
	return nil
}

// usageEventColumns is every column a UsageEvent is read back from, in the
// order scanUsageEvent takes them.
//
// One list, shared by every query that returns whole events, because each
// query used to keep its own: ChatEvents' copy stopped at error, so the chat
// view sent the same request rows as the request log with error_code,
// rejected, ip and virtual_model always empty. A column added to the schema
// is added here and in RecordUsage, and nowhere else.
//
// Selected FROM usage_events, unaliased: the names are subqueries that name
// the table, so the statements around this keep their bare column names.
var usageEventColumns = `id, at, key_id, COALESCE(` + nameOf(nameKey, "usage_events.key_id") + `, ''),
	account_id, COALESCE(` + nameOf(nameAccount, "usage_events.account_id") + `, ''), model, path,
	conversation_id, client,
	status, streaming, input_tokens, output_tokens,
	cache_read_tokens, cache_write_tokens, duration_ms, first_token_ms, error,
	error_code, kind, ip, alias, provider, reasoning_tokens`

// scanUsageEvent reads one row selected with usageEventColumns.
func scanUsageEvent(sc scanner) (UsageEvent, error) {
	var e UsageEvent
	var at, durMS, firstMS int64
	var kind int
	if err := sc.Scan(&e.ID, &at, &e.KeyID, &e.KeyName, &e.AccountID, &e.AccountEmail,
		&e.Model, &e.Path, &e.ConversationID, &e.Client, &e.Status, &e.Streaming,
		&e.InputTokens, &e.OutputTokens, &e.CacheReadTokens, &e.CacheWriteTokens,
		&durMS, &firstMS, &e.Error, &e.ErrorCode, &kind, &e.IP, &e.VirtualModel,
		&e.Provider, &e.ReasoningTokens); err != nil {
		return UsageEvent{}, err
	}
	e.At = fromMS(at)
	e.Rejected = kind == kindRejected
	e.Duration = time.Duration(durMS) * time.Millisecond
	e.FirstToken = time.Duration(firstMS) * time.Millisecond
	return e, nil
}

// KeyUsage totals each key's traffic since a point in time, keyed by key id.
// Used by the keys list, so a key that is quietly doing nothing is visible as
// such.
func (s *Store) KeyUsage(ctx context.Context, since time.Time) (map[string]UsageBucket, error) {
	return s.usageBy(ctx, "key_id", since)
}

// AccountUsage totals each account's traffic since a point in time, keyed by
// account id.
func (s *Store) AccountUsage(ctx context.Context, since time.Time) (map[string]UsageBucket, error) {
	return s.usageBy(ctx, "account_id", since)
}

// usageBy totals traffic since a point in time grouped by one id column,
// keyed by that id. The column is a constant from the caller, never anything a
// request carries: it is the one part of the statement that cannot be a
// parameter.
func (s *Store) usageBy(ctx context.Context, column string, since time.Time) (map[string]UsageBucket, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+column+`, COUNT(*),
		        COALESCE(SUM(`+sqlIsError+`), 0),
		        COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(`+sqlCacheTokens+`), 0)
		   FROM usage_events WHERE `+sqlCounted+` AND at >= ? GROUP BY `+column,
		ms(since))
	if err != nil {
		return nil, fmt.Errorf("usage by %s: %w", column, err)
	}
	defer rows.Close()

	out := map[string]UsageBucket{}
	for rows.Next() {
		var b UsageBucket
		if err := rows.Scan(&b.ID, &b.Requests, &b.Errors,
			&b.InputTokens, &b.OutputTokens, &b.CacheTokens); err != nil {
			return nil, fmt.Errorf("usage by %s: %w", column, err)
		}
		// The label is the id: these maps are joined onto keys and accounts
		// by the caller, which has the names.
		b.Label = b.ID
		out[b.ID] = b
	}
	return out, rows.Err()
}

// KeyWeights sums what each key sent one account since a time, weighted by
// what a token of each kind costs, keyed by key id.
//
// Weighted rather than counted because a subscription's limits track cost,
// not tokens, and the kinds differ by two orders of magnitude: Anthropic's
// price list holds output at 5x input, a cache write at 1.25x and a cache read
// at 0.1x, for every model. A key that re-reads a cached 160k-token prompt is
// not spending what one sending 160k fresh tokens is. Model is not weighted:
// the ratio between models is not a constant the limits are known to follow.
func (s *Store) KeyWeights(ctx context.Context, accountID string, since time.Time) (map[string]float64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key_id,
		        SUM(input_tokens + 5.0*output_tokens + 1.25*cache_write_tokens + 0.1*cache_read_tokens)
		   FROM usage_events
		  WHERE `+sqlCounted+` AND account_id = ? AND at >= ?
		  GROUP BY key_id`, accountID, ms(since))
	if err != nil {
		return nil, fmt.Errorf("key weights: %w", err)
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var id string
		var w float64
		if err := rows.Scan(&id, &w); err != nil {
			return nil, fmt.Errorf("key weights: %w", err)
		}
		out[id] = w
	}
	return out, rows.Err()
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
		`SELECT COALESCE(SUM(`+sqlTokens+`), 0)
		   FROM usage_events WHERE `+sqlCounted+` AND at >= ? AND key_id = ?`,
		ms(since), keyID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("key spend: %w", err)
	}
	return n, nil
}

// OldestSpendAt is when the earliest still-counted request for a key happened,
// so a refusal can say when the budget starts freeing up rather than telling
// the client to guess.
func (s *Store) OldestSpendAt(ctx context.Context, keyID string, since time.Time) (time.Time, bool) {
	var at sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT MIN(at) FROM usage_events
		  WHERE `+sqlCounted+` AND at >= ? AND key_id = ?
		    AND `+sqlTokens+` > 0`,
		ms(since), keyID).Scan(&at)
	if err != nil || !at.Valid {
		return time.Time{}, false
	}
	return fromMS(at.Int64), true
}
