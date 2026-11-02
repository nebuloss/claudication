// Package recordings keeps recorded content on disk: what an operator
// recorded of a client (internal/service/capture) outlives the process that
// recorded it, for as long as capture.retention-days says and within
// capture.disk-budget.
//
// It is a file of its own beside the gateway's database, not tables in it.
// Its writes are large and come in bursts, and in the main database they
// would hold the one write lock the request log and every setting share; its
// lifetime is not the database's — it is left out of backups, and a restore
// discards it — and it can be thrown away whole at any time, which is what
// keeps its format free to change: the file's name carries its version
// (FileName), so a release that changes the format starts a new file beside
// the old one rather than migrating it, and the old one is removed once the
// release serving alone is the only one left (RemoveOthers).
//
// It stores what it is given and knows nothing of what it means. A body is a
// list of chunks (capture cuts them by content, so a conversation resent on
// every turn shares its chunks with the turn before), each kept once by its
// hash; a recording is its request log row's id, its client, its chat, a
// sealed description of everything but its bodies, and the hashes it uses.
// Sealing, compressing and cutting are the caller's: this package never sees
// plaintext, and needs no key.
package recordings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"claudication/internal/live"

	_ "modernc.org/sqlite"
)

// Version is the stored format's. A change to the schema, or to how capture
// cuts, compresses or seals what it stores, is a new version: a new file.
const Version = 1

// FileName is the file a version is kept in.
func FileName(version int) string { return fmt.Sprintf("capture-v%d.db", version) }

// ErrNotFound is a recording that is not stored.
var ErrNotFound = errors.New("no such recording")

const schema = `
CREATE TABLE IF NOT EXISTS meta (
    k TEXT PRIMARY KEY,
    v BLOB NOT NULL
) WITHOUT ROWID;

-- One chunk of one or more bodies, sealed, under its hash.
CREATE TABLE IF NOT EXISTS chunks (
    hash BLOB PRIMARY KEY,
    data BLOB NOT NULL
) WITHOUT ROWID;

-- One recorded request, by its request log row.
CREATE TABLE IF NOT EXISTS recordings (
    event        INTEGER PRIMARY KEY,
    at           INTEGER NOT NULL,
    key_id       TEXT    NOT NULL,
    conversation TEXT    NOT NULL DEFAULT '',
    -- Everything but the bodies, sealed: the legs, their headers, and each
    -- body as its list of chunk hashes.
    meta         BLOB    NOT NULL,
    -- The bodies' plain size together, as recorded.
    bytes        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS recordings_at ON recordings(at);
CREATE INDEX IF NOT EXISTS recordings_key ON recordings(key_id, event);

-- The chunks each recording uses, once each: what keeps a chunk alive.
CREATE TABLE IF NOT EXISTS refs (
    event INTEGER NOT NULL,
    hash  BLOB    NOT NULL,
    PRIMARY KEY (event, hash)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS refs_hash ON refs(hash);
`

// DB is one open file of recordings.
type DB struct {
	// write is one connection, so writes queue here rather than contend for
	// the file's lock; read is a pool, which WAL lets read beside it.
	write, read *sql.DB
	path        string
	hub         *live.Hub
}

func dsn(path string) string {
	// The main database's pragmas (internal/store, dsn), with a smaller
	// journal kept: the WAL counts against the disk budget.
	return "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=auto_vacuum(incremental)" +
		"&_pragma=temp_store(memory)" +
		"&_pragma=journal_size_limit(16777216)" +
		"&_txlock=immediate"
}

// Open opens the file at path, creating it, and tells hub of every change
// to what it holds. Two processes may hold it at once — the two workers of a
// handover — and both may write: a recording is its request log row's, which
// only one of them made, and a chunk is the same bytes whoever stores it.
func Open(ctx context.Context, path string, hub *live.Hub) (*DB, error) {
	write, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open recordings: %w", err)
	}
	write.SetMaxOpenConns(1)
	if _, err := write.ExecContext(ctx, schema); err != nil {
		write.Close()
		return nil, fmt.Errorf("open recordings %s: %w", path, err)
	}
	read, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		write.Close()
		return nil, fmt.Errorf("open recordings: %w", err)
	}
	read.SetMaxOpenConns(4)
	// Sealed content: the state directory is the outer guard, this the inner.
	if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
		write.Close()
		read.Close()
		return nil, fmt.Errorf("secure recordings file: %w", err)
	}
	return &DB{write: write, read: read, path: path, hub: hub}, nil
}

// Close closes the file.
func (d *DB) Close() error {
	return errors.Join(d.write.Close(), d.read.Close())
}

// Path is the file's.
func (d *DB) Path() string { return d.path }

// Meta is one of the file's own values.
func (d *DB) Meta(ctx context.Context, k string) ([]byte, bool, error) {
	var v []byte
	err := d.read.QueryRowContext(ctx, `SELECT v FROM meta WHERE k = ?`, k).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("recordings meta %s: %w", k, err)
	}
	return v, true, nil
}

// SetMeta sets one of the file's own values.
func (d *DB) SetMeta(ctx context.Context, k string, v []byte) error {
	//live:none — the file's own bookkeeping, which no screen shows.
	if _, err := d.write.ExecContext(ctx, `INSERT INTO meta (k, v) VALUES (?, ?)
		ON CONFLICT (k) DO UPDATE SET v = excluded.v`, k, v); err != nil {
		return fmt.Errorf("recordings meta %s: %w", k, err)
	}
	return nil
}

// Recording is one stored request, without its chunks.
type Recording struct {
	Event        int64
	At           time.Time
	KeyID        string
	Conversation string
	// Meta is sealed by the caller; this package does not read it.
	Meta  []byte
	Bytes int64
	// Hashes are the chunks it uses, each once.
	Hashes [][]byte
}

// Missing is which of hashes are not stored yet: the chunks a caller has to
// seal and send, out of all a recording uses.
func (d *DB) Missing(ctx context.Context, hashes [][]byte) (map[string]bool, error) {
	out := make(map[string]bool, len(hashes))
	for _, h := range hashes {
		out[string(h)] = true
	}
	for part := range batches(hashes, 500) {
		rows, err := d.read.QueryContext(ctx,
			`SELECT hash FROM chunks WHERE hash IN (?`+strings.Repeat(",?", len(part)-1)+`)`, anys(part)...)
		if err != nil {
			return nil, fmt.Errorf("recordings: %w", err)
		}
		for rows.Next() {
			var h []byte
			if err := rows.Scan(&h); err != nil {
				rows.Close()
				return nil, fmt.Errorf("recordings: %w", err)
			}
			delete(out, string(h))
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("recordings: %w", err)
		}
	}
	return out, nil
}

// Put stores a recording and the chunks it brings, sealed by the caller and
// keyed by hash: one transaction, so a recording is never stored without the
// chunks it uses. A chunk already there is kept as it is.
func (d *DB) Put(ctx context.Context, r Recording, chunks map[string][]byte) error {
	defer d.hub.Changed(live.Captures)
	tx, err := d.write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store recording %d: %w", r.Event, err)
	}
	defer tx.Rollback()
	for h, data := range chunks {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO chunks (hash, data) VALUES (?, ?)`, []byte(h), data); err != nil {
			return fmt.Errorf("store recording %d: %w", r.Event, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO recordings (event, at, key_id, conversation, meta, bytes)
		VALUES (?, ?, ?, ?, ?, ?)`, r.Event, r.At.UnixMilli(), r.KeyID, r.Conversation, r.Meta, r.Bytes); err != nil {
		return fmt.Errorf("store recording %d: %w", r.Event, err)
	}
	for _, h := range r.Hashes {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO refs (event, hash) VALUES (?, ?)`, r.Event, h); err != nil {
			return fmt.Errorf("store recording %d: %w", r.Event, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store recording %d: %w", r.Event, err)
	}
	return nil
}

// Has is which of events have a recording stored.
func (d *DB) Has(ctx context.Context, events []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	for part := range batches(events, 500) {
		rows, err := d.read.QueryContext(ctx,
			`SELECT event FROM recordings WHERE event IN (?`+strings.Repeat(",?", len(part)-1)+`)`, anys(part)...)
		if err != nil {
			return nil, fmt.Errorf("recordings: %w", err)
		}
		for rows.Next() {
			var e int64
			if err := rows.Scan(&e); err != nil {
				rows.Close()
				return nil, fmt.Errorf("recordings: %w", err)
			}
			out[e] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("recordings: %w", err)
		}
	}
	return out, nil
}

// Get is one stored recording, without its chunks (Chunks reads them).
func (d *DB) Get(ctx context.Context, event int64) (Recording, error) {
	r := Recording{Event: event}
	var at int64
	err := d.read.QueryRowContext(ctx,
		`SELECT at, key_id, conversation, meta, bytes FROM recordings WHERE event = ?`, event).
		Scan(&at, &r.KeyID, &r.Conversation, &r.Meta, &r.Bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return Recording{}, ErrNotFound
	}
	if err != nil {
		return Recording{}, fmt.Errorf("recording %d: %w", event, err)
	}
	r.At = time.UnixMilli(at)
	return r, nil
}

// Chunks reads chunks by hash, sealed as they were stored.
func (d *DB) Chunks(ctx context.Context, hashes [][]byte) (map[string][]byte, error) {
	out := make(map[string][]byte, len(hashes))
	for part := range batches(hashes, 500) {
		rows, err := d.read.QueryContext(ctx,
			`SELECT hash, data FROM chunks WHERE hash IN (?`+strings.Repeat(",?", len(part)-1)+`)`, anys(part)...)
		if err != nil {
			return nil, fmt.Errorf("recording chunks: %w", err)
		}
		for rows.Next() {
			var h, data []byte
			if err := rows.Scan(&h, &data); err != nil {
				rows.Close()
				return nil, fmt.Errorf("recording chunks: %w", err)
			}
			out[string(h)] = data
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("recording chunks: %w", err)
		}
	}
	return out, nil
}

// Clear drops the recordings of a client's key, or every one when keyID is
// empty, and the chunks no recording uses any more.
func (d *DB) Clear(ctx context.Context, keyID string) error {
	defer d.hub.Changed(live.Captures)
	where, args := "", []any{}
	if keyID != "" {
		where, args = " WHERE key_id = ?", []any{keyID}
	}
	if err := d.drop(ctx, `SELECT event FROM recordings`+where, args...); err != nil {
		return err
	}
	return d.sweep(ctx)
}

// Reset empties the file: every recording, every chunk and its own values.
// For a file whose recordings cannot be opened any more — sealed under
// another key.
func (d *DB) Reset(ctx context.Context) error {
	defer d.hub.Changed(live.Captures)
	if _, err := d.write.ExecContext(ctx, `DELETE FROM refs; DELETE FROM recordings; DELETE FROM chunks; DELETE FROM meta;`); err != nil {
		return fmt.Errorf("reset recordings: %w", err)
	}
	return d.compact(ctx)
}

// drop deletes the recordings the query selects, and their refs.
func (d *DB) drop(ctx context.Context, selectEvents string, args ...any) error {
	//live:none — its callers mark the topic.
	tx, err := d.write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("drop recordings: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM refs WHERE event IN (`+selectEvents+`)`, args...); err != nil {
		return fmt.Errorf("drop recordings: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM recordings WHERE event IN (`+selectEvents+`)`, args...); err != nil {
		return fmt.Errorf("drop recordings: %w", err)
	}
	return tx.Commit()
}

// sweep deletes the chunks no recording uses, a batch at a time so no one
// transaction holds the file for long.
func (d *DB) sweep(ctx context.Context) error {
	//live:none — what it removes no recording uses, so no screen shows it.
	for {
		res, err := d.write.ExecContext(ctx, `DELETE FROM chunks WHERE hash IN (
			SELECT c.hash FROM chunks c WHERE NOT EXISTS (SELECT 1 FROM refs r WHERE r.hash = c.hash) LIMIT 2000)`)
		if err != nil {
			return fmt.Errorf("sweep recordings: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
	}
}

// compact hands freed pages back to the filesystem and trims the journal.
func (d *DB) compact(ctx context.Context) error {
	if _, err := d.write.ExecContext(ctx, `PRAGMA incremental_vacuum`); err != nil {
		return fmt.Errorf("compact recordings: %w", err)
	}
	if _, err := d.write.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("compact recordings: %w", err)
	}
	return nil
}

// Usage is what the file holds.
type Usage struct {
	// Bytes is the file's pages in use and its journal.
	Bytes int64 `json:"bytes"`
	// Content is the stored chunks' size, sealed and compressed: what the
	// recordings take once each part shared between them is counted once.
	Content    int64     `json:"content"`
	Recordings int64     `json:"recordings"`
	Oldest     time.Time `json:"oldest,omitzero"`
}

// Usage reports what the file holds.
func (d *DB) Usage(ctx context.Context) (Usage, error) {
	var u Usage
	var pages, free, size int64
	for q, dst := range map[string]*int64{
		`PRAGMA page_count`: &pages, `PRAGMA freelist_count`: &free, `PRAGMA page_size`: &size,
	} {
		if err := d.read.QueryRowContext(ctx, q).Scan(dst); err != nil {
			return Usage{}, fmt.Errorf("recordings usage: %w", err)
		}
	}
	u.Bytes = (pages - free) * size
	if st, err := os.Stat(d.path + "-wal"); err == nil {
		u.Bytes += st.Size()
	}
	var oldest sql.NullInt64
	if err := d.read.QueryRowContext(ctx, `SELECT COUNT(*), MIN(at) FROM recordings`).Scan(&u.Recordings, &oldest); err != nil {
		return Usage{}, fmt.Errorf("recordings usage: %w", err)
	}
	if oldest.Valid {
		u.Oldest = time.UnixMilli(oldest.Int64)
	}
	if err := d.read.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(data)), 0) FROM chunks`).Scan(&u.Content); err != nil {
		return Usage{}, fmt.Errorf("recordings usage: %w", err)
	}
	return u, nil
}

// pruneBatch is how many of the oldest recordings go at a time when the file
// is over its budget: measured again after each, so it stops near the line.
const pruneBatch = 50

// Prune drops recordings older than keep, then the oldest until the file is
// within budget, and the chunks nothing uses; it returns how many went.
func (d *DB) Prune(ctx context.Context, keep time.Duration, budget int64) (int64, error) {
	defer d.hub.Changed(live.Captures)
	var gone int64
	count := func() int64 {
		var n int64
		_ = d.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM recordings`).Scan(&n)
		return n
	}
	before := count()
	if keep > 0 {
		cutoff := time.Now().Add(-keep).UnixMilli()
		if err := d.drop(ctx, `SELECT event FROM recordings WHERE at < ?`, cutoff); err != nil {
			return 0, err
		}
	}
	if err := d.sweep(ctx); err != nil {
		return 0, err
	}
	if err := d.compact(ctx); err != nil {
		return 0, err
	}
	for budget > 0 {
		u, err := d.Usage(ctx)
		if err != nil {
			return 0, err
		}
		if u.Bytes <= budget || u.Recordings == 0 {
			break
		}
		if err := d.drop(ctx, `SELECT event FROM recordings ORDER BY at, event LIMIT ?`, pruneBatch); err != nil {
			return 0, err
		}
		if err := d.sweep(ctx); err != nil {
			return 0, err
		}
		if err := d.compact(ctx); err != nil {
			return 0, err
		}
	}
	gone = before - count()
	return gone, nil
}

// RemoveOthers deletes the files of every other version in dir: what a
// release that changed the format left behind. Only the worker serving alone
// calls it, once the one before it is gone; a file another process still has
// open is unlinked under it, which on the systems served is harmless.
func RemoveOthers(dir string) {
	matches, _ := filepath.Glob(filepath.Join(dir, "capture-v*.db"))
	keep := FileName(Version)
	for _, m := range matches {
		if filepath.Base(m) == keep {
			continue
		}
		for _, suffix := range []string{"", "-wal", "-shm"} {
			_ = os.Remove(m + suffix)
		}
	}
}

// RemoveAll deletes every version's file in dir: what restoring a backup
// does, since the recordings belonged to the database it replaced.
func RemoveAll(dir string) {
	matches, _ := filepath.Glob(filepath.Join(dir, "capture-v*.db*"))
	for _, m := range matches {
		_ = os.Remove(m)
	}
}

func batches[T any](all []T, n int) func(func([]T) bool) {
	return func(yield func([]T) bool) {
		for len(all) > 0 {
			k := min(n, len(all))
			if !yield(all[:k]) {
				return
			}
			all = all[k:]
		}
	}
}

func anys[T any](xs []T) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}
