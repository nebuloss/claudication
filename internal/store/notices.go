package store

import (
	"context"
	"fmt"
	"time"

	"claudication/internal/live"
)

// NoticesRead are the notifications the operator has marked read: when, by
// notice id.
func (s *Store) NoticesRead(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, read_at FROM notices_read`)
	if err != nil {
		return nil, fmt.Errorf("read notice marks: %w", err)
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var (
			id string
			at int64
		)
		if err := rows.Scan(&id, &at); err != nil {
			return nil, fmt.Errorf("read notice marks: %w", err)
		}
		out[id] = fromMS(at)
	}
	return out, rows.Err()
}

// MarkNotices marks the notices with these ids read, or unread with read
// false. Marking one read again keeps when it was first read.
func (s *Store) MarkNotices(ctx context.Context, ids []string, read bool) error {
	defer s.changed(live.Inbox)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mark notices: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := ms(time.Now())
	for _, id := range ids {
		if read {
			_, err = tx.ExecContext(ctx,
				`INSERT INTO notices_read (id, read_at) VALUES (?, ?) ON CONFLICT(id) DO NOTHING`, id, now)
		} else {
			_, err = tx.ExecContext(ctx, `DELETE FROM notices_read WHERE id = ?`, id)
		}
		if err != nil {
			return fmt.Errorf("mark notices: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mark notices: %w", err)
	}
	return nil
}

// ForgetNoticesRead drops the marks of every notice not among current: its
// problem is gone, and if it comes back it is news again.
func (s *Store) ForgetNoticesRead(ctx context.Context, current map[string]bool) error {
	marks, err := s.NoticesRead(ctx)
	if err != nil {
		return err
	}
	var gone []string
	for id := range marks {
		if !current[id] {
			gone = append(gone, id)
		}
	}
	if len(gone) == 0 {
		return nil
	}
	// MarkNotices marks the topic.
	return s.MarkNotices(ctx, gone, false)
}
