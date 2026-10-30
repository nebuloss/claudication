package store

import (
	"context"
	"fmt"
	"time"

	"claudication/internal/live"
)

// PruneUsage drops events older than the retention window and reports how many
// went. Retention is the only thing keeping this table bounded.
func (s *Store) PruneUsage(ctx context.Context, keep time.Duration) (int64, error) {
	defer s.changed(live.Requests)
	if keep <= 0 {
		return 0, nil
	}
	cutoff := ms(time.Now().Add(-keep))
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

// Optimize lets SQLite refresh the statistics the planner chooses indexes by,
// for the tables whose contents have moved enough since they were last
// gathered. Run after the daily prune, which is what moves them most.
//
// 0x10002 rather than the bare pragma: the bare form only considers tables
// the connection running it has queried, and in a pool of four that is
// whichever connection the call lands on. It is cheap when nothing changed,
// and bounded when something did (SQLite caps how much of a table it reads).
func (s *Store) Optimize(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA optimize=0x10002`); err != nil {
		return fmt.Errorf("optimize: %w", err)
	}
	return nil
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
