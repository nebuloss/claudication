package store

import (
	"context"
	"fmt"
	"time"
)

// UptimeSample is one minute the gateway was running, and how it was: see
// uptime_samples in schema.sql. What the states mean belongs to
// internal/service/uptime; the store keeps the number.
type UptimeSample struct {
	Minute time.Time
	State  int
}

// RecordUptime notes the state the gateway was in at at, in the minute that
// contains it. A second sample in the same minute keeps the worse of the two,
// so a blip between two ticks is not overwritten by the recovery after it.
func (s *Store) RecordUptime(ctx context.Context, at time.Time, state int) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO uptime_samples (minute, state) VALUES (?, ?)
		 ON CONFLICT (minute) DO UPDATE SET state = MAX(state, excluded.state)`,
		ms(at.Truncate(time.Minute)), state)
	if err != nil {
		return fmt.Errorf("record uptime: %w", err)
	}
	return nil
}

// UptimeSince is every sample from from on, oldest first.
func (s *Store) UptimeSince(ctx context.Context, from time.Time) ([]UptimeSample, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT minute, state FROM uptime_samples WHERE minute >= ? ORDER BY minute`, ms(from))
	if err != nil {
		return nil, fmt.Errorf("read uptime: %w", err)
	}
	defer rows.Close()
	var out []UptimeSample
	for rows.Next() {
		var minute int64
		var u UptimeSample
		if err := rows.Scan(&minute, &u.State); err != nil {
			return nil, fmt.Errorf("read uptime: %w", err)
		}
		u.Minute = fromMS(minute)
		out = append(out, u)
	}
	return out, rows.Err()
}

// PruneUptime drops the samples from before before, and reports how many went.
func (s *Store) PruneUptime(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM uptime_samples WHERE minute < ?`, ms(before))
	if err != nil {
		return 0, fmt.Errorf("prune uptime: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// UptimeOutage is a stretch the gateway was not taking requests: see
// uptime_outages in schema.sql. End is zero while it has not ended.
type UptimeOutage struct {
	Start, End time.Time
}

// BeginOutage notes that the gateway stopped taking requests at at.
func (s *Store) BeginOutage(ctx context.Context, at time.Time) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO uptime_outages (started, ended) VALUES (?, NULL) ON CONFLICT (started) DO NOTHING`,
		ms(at)); err != nil {
		return fmt.Errorf("record outage: %w", err)
	}
	return nil
}

// EndOutages closes every outage still open at at: the gateway takes requests
// again.
func (s *Store) EndOutages(ctx context.Context, at time.Time) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE uptime_outages SET ended = ? WHERE ended IS NULL AND started <= ?`, ms(at), ms(at)); err != nil {
		return fmt.Errorf("end outage: %w", err)
	}
	return nil
}

// OutagesSince is every outage that ended at or after from, or has not ended,
// oldest first.
func (s *Store) OutagesSince(ctx context.Context, from time.Time) ([]UptimeOutage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT started, ended FROM uptime_outages WHERE ended IS NULL OR ended >= ? ORDER BY started`, ms(from))
	if err != nil {
		return nil, fmt.Errorf("read outages: %w", err)
	}
	defer rows.Close()
	var out []UptimeOutage
	for rows.Next() {
		var start int64
		var end *int64
		if err := rows.Scan(&start, &end); err != nil {
			return nil, fmt.Errorf("read outages: %w", err)
		}
		o := UptimeOutage{Start: fromMS(start)}
		if end != nil {
			o.End = fromMS(*end)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// PruneOutages drops the outages that ended before before.
func (s *Store) PruneOutages(ctx context.Context, before time.Time) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM uptime_outages WHERE ended IS NOT NULL AND ended < ?`, ms(before)); err != nil {
		return fmt.Errorf("prune outages: %w", err)
	}
	return nil
}
