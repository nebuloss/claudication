package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ClientOutcome is one client's requests since a time, as the Overview's
// notices read them: how many failed, and whether the latest ones are failing
// one after another.
type ClientOutcome struct {
	KeyID    string
	Requests int
	Failures int
	// Streak is how many of its latest requests failed in a row: 0 when the
	// last one worked.
	Streak int
	// Status, Code and Error are its latest failure's, and LastFailed when.
	Status     int
	Code       string
	Error      string
	LastFailed time.Time
}

// ClientOutcomes are the clients' relayed requests since a time, by key: a
// failure as the request log counts one (sqlIsError). Read in order so the
// streak is the run of failures the client is in now.
func (s *Store) ClientOutcomes(ctx context.Context, since time.Time) (map[string]*ClientOutcome, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key_id, at, status, error_code, error FROM usage_events
		  WHERE at >= ? AND kind = 0 AND key_id <> ''
		  ORDER BY at, id`, ms(since))
	if err != nil {
		return nil, fmt.Errorf("client outcomes: %w", err)
	}
	defer rows.Close()
	out := map[string]*ClientOutcome{}
	for rows.Next() {
		var (
			key, code, msg string
			at             int64
			status         int
		)
		if err := rows.Scan(&key, &at, &status, &code, &msg); err != nil {
			return nil, fmt.Errorf("client outcomes: %w", err)
		}
		o, ok := out[key]
		if !ok {
			o = &ClientOutcome{KeyID: key}
			out[key] = o
		}
		o.Requests++
		if code == "" {
			o.Streak = 0
			continue
		}
		o.Failures++
		o.Streak++
		o.Status, o.Code, o.Error, o.LastFailed = status, code, msg, fromMS(at)
	}
	return out, rows.Err()
}

// CodeSeen is how many requests since a time failed with one error code, and
// when the latest did.
func (s *Store) CodeSeen(ctx context.Context, code string, since time.Time) (int, time.Time, error) {
	var (
		n    int
		last sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), MAX(at) FROM usage_events WHERE error_code = ? AND at >= ?`,
		code, ms(since)).Scan(&n, &last)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("code seen: %w", err)
	}
	if !last.Valid {
		return n, time.Time{}, nil
	}
	return n, fromMS(last.Int64), nil
}
