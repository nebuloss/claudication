package store

import (
	"context"
	"fmt"
	"time"
)

// KeyRequest is one request a key sent, as a client's Limits page reads its
// history: when, and the tokens it was charged.
type KeyRequest struct {
	At     time.Time
	Tokens int64
}

// KeyRequests is every request a key sent since a moment, oldest first: the
// ones relayed, and the ones the gateway refused under its name — a request
// the rate limit turned away was still sent, and a limit read off only the
// requests that got through would be read off a history the old limit had
// already cut down.
//
// A range on idx_usage_at narrowed by key, as KeySpend is: there is no index on
// key_id (see schema.sql), and a client's page asks for a few weeks at most.
func (s *Store) KeyRequests(ctx context.Context, keyID string, since time.Time) ([]KeyRequest, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT at, `+sqlTokens+` FROM usage_events
		  WHERE at >= ? AND key_id = ?
		  ORDER BY at, id`, ms(since), keyID)
	if err != nil {
		return nil, fmt.Errorf("key requests: %w", err)
	}
	defer rows.Close()
	var out []KeyRequest
	for rows.Next() {
		var (
			at     int64
			tokens int64
		)
		if err := rows.Scan(&at, &tokens); err != nil {
			return nil, fmt.Errorf("key requests: %w", err)
		}
		out = append(out, KeyRequest{At: fromMS(at), Tokens: tokens})
	}
	return out, rows.Err()
}

// MinuteCount is how many requests the gateway took in one minute.
type MinuteCount struct {
	// Minute is the minute's start on the Unix minute, which is what
	// time.Truncate(time.Minute) gives, so a caller's own minutes line up.
	Minute   time.Time
	Requests int
}

// RequestsPerMinute is every request the gateway took since a moment, counted
// by the minute, oldest first and only the minutes that had any: Activity's
// live strip, the gateway-wide twin of a client's from KeyRequests. Counted as
// that one is, refusals included: the strip is the load arriving, not what got
// through.
//
// Grouped in SQL rather than read row by row as KeyRequests is: one key's hour
// is a handful of rows, the whole gateway's can be thousands, and only the
// counts are wanted. A range on idx_usage_at.
func (s *Store) RequestsPerMinute(ctx context.Context, since time.Time) ([]MinuteCount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT at / 60000, COUNT(*) FROM usage_events
		  WHERE at >= ?
		  GROUP BY 1 ORDER BY 1`, ms(since))
	if err != nil {
		return nil, fmt.Errorf("requests per minute: %w", err)
	}
	defer rows.Close()
	out := []MinuteCount{}
	for rows.Next() {
		var (
			minute int64
			n      int
		)
		if err := rows.Scan(&minute, &n); err != nil {
			return nil, fmt.Errorf("requests per minute: %w", err)
		}
		out = append(out, MinuteCount{Minute: fromMS(minute * 60000), Requests: n})
	}
	return out, rows.Err()
}
