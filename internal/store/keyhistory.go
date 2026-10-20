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
