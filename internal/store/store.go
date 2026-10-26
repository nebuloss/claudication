// Package store owns claudication's durable state.
//
// SQLite via modernc.org/sqlite: a pure-Go driver, so the binary builds with
// CGO_ENABLED=0 and ships in a scratch image with no runtime at all.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"claudication/internal/secret"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type Store struct {
	db *sql.DB
	// sealer seals every credential the store keeps: account tokens and the
	// secrets of api keys. Given once, to Open, rather than to each method that
	// needs it: the store is the only thing that reads or writes a sealed
	// column, and handing the sealer to every caller meant every caller could
	// forget a step — keeping a new key's secret was a second call each place
	// a key was made.
	sealer *secret.Sealer
	// keysUsed and accountsUsed hold TouchKey and MarkAccountUsed to one
	// write a minute per id; see lastUse.
	keysUsed, accountsUsed lastUse
	// compactErr is what reclaim returned when the store was opened.
	compactErr error
}

// Open connects to the database at path and brings the schema up to date.
// sealer is what the database's credentials are sealed with.
func Open(ctx context.Context, dbPath string, sealer *secret.Sealer) (*Store, error) {
	return open(ctx, dbPath, sealer, false)
}

// ErrSchemaChange is OpenAlongside refusing: the database is not at the schema
// this binary serves, and bringing it there would change it under another
// process still serving from it.
var ErrSchemaChange = errors.New("the database needs a schema change")

// OpenAlongside opens the database while another process serves from it — a
// worker started beside the one it is taking over from (internal/handover).
//
// Two processes writing one WAL database is safe; one changing its schema
// under the other is not, because the other goes on writing what the old
// schema expects. So this never migrates: a database at any version but
// schemaVersion, or an empty one, is ErrSchemaChange, and the caller falls
// back to stopping the other process first. Nor does it reclaim: VACUUM would
// hold the write lock for the length of a rewrite, past the busy timeout the
// serving process waits for it, and nothing was rebuilt to reclaim.
func OpenAlongside(ctx context.Context, dbPath string, sealer *secret.Sealer) (*Store, error) {
	return open(ctx, dbPath, sealer, true)
}

func open(ctx context.Context, dbPath string, sealer *secret.Sealer, alongside bool) (*Store, error) {
	if sealer == nil {
		return nil, errors.New("open database: no sealer")
	}
	db, err := sql.Open("sqlite", dsn(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// SQLite takes one writer at a time; a small pool avoids lock churn.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to database %s: %w", dbPath, err)
	}

	// Whichever command opens the store first after an update runs the
	// upgrade, and the short commands carry short deadlines: login-url has
	// 15 s. A rebuild of a large usage table outlasts that, and cutting it off
	// gains nothing — the transaction rolls back and the next command starts
	// it again from the top. So the upgrade runs to the end once begun.
	mctx := context.WithoutCancel(ctx)
	var compactErr error
	if alongside {
		version, empty, err := schemaVersionOf(ctx, db)
		if err == nil && (empty || version != schemaVersion) {
			err = fmt.Errorf("%w: it is at version %d, this release serves %d", ErrSchemaChange, version, schemaVersion)
		}
		if err != nil {
			db.Close()
			return nil, err
		}
	} else {
		if err := migrate(mctx, db); err != nil {
			db.Close()
			return nil, err
		}
		compactErr = reclaim(mctx, db)
	}

	// SQLite creates the file honouring umask, which typically yields 0644.
	// It holds sealed credentials, so tighten it: the 0700 state directory is
	// the outer guard, this is the inner one.
	if err := os.Chmod(dbPath, 0o600); err != nil && !os.IsNotExist(err) {
		db.Close()
		return nil, fmt.Errorf("secure database file: %w", err)
	}

	return &Store{db: db, sealer: sealer, compactErr: compactErr}, nil
}

// CompactErr is why handing back the space an upgrade freed failed when this
// store was opened, or nil. It is not a reason to refuse to serve: the data is
// current, and the next open tries again.
func (s *Store) CompactErr() error { return s.compactErr }

// dsn is how every connection to the database at dbPath is opened.
func dsn(dbPath string) string {
	// WAL keeps readers from blocking the writer; busy_timeout stops a
	// concurrent write from failing outright under load.
	//
	// auto_vacuum=incremental so the file can give space back. Without it
	// SQLite keeps freed pages for reuse and the file only ever grows: pruning
	// a month of usage events, or lowering retention-days, reclaims nothing and
	// the database stays at its historical peak for the life of the install.
	// "incremental" rather than "full" because full auto-vacuum reorganises on
	// every commit, which is a cost on the write path of every relayed request;
	// incremental does nothing until asked, and the pruner asks once a day.
	//
	// It only takes on an empty database. An existing one keeps whatever it was
	// created with until it is rewritten — see Vacuum, and reclaim, which
	// applies it to a file an upgrade rebuilt.
	//
	// temp_store=memory: the sorts and groupings the reports and the filter
	// menus cannot take from an index build their temporary B-trees in memory
	// rather than in a file beside the database; they are a few thousand
	// groups, not the table.
	//
	// journal_size_limit: a checkpoint leaves the WAL file at its peak size,
	// and one VACUUM or a month's prune grows it to the size of what it
	// rewrote. 64 MiB is kept for reuse and the rest truncated.
	//
	// _txlock=immediate: every transaction here writes, and BEGIN IMMEDIATE
	// takes the write lock at the start, where busy_timeout waits for it. A
	// deferred one that read first and then wrote could fail SQLITE_BUSY at
	// the write with no wait at all, because in WAL a reader's snapshot cannot
	// be upgraded once another writer has committed.
	return "file:" + dbPath +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=auto_vacuum(incremental)" +
		"&_pragma=temp_store(memory)" +
		"&_pragma=journal_size_limit(67108864)" +
		"&_txlock=immediate"
}

// schemaVersion is the version schema.sql describes, and the one every
// database is brought to before the gateway serves from it.
//
// How schema_migrations records it: one row per step applied since the
// database reached a version this binary knows, and the version is the highest
// row. A fresh database is created whole from schema.sql and records
// schemaVersion alone; an upgraded one records each step it took. The rows for
// 001-028 went with the files they named (see migrations/029): history that
// no binary can replay is not history worth keeping.
const schemaVersion = 42

// oldestUpgradable is the earliest version an upgrade starts from. 001-028 were
// folded into schema.sql, so a database from before 028 has no path from here;
// it goes through a release that still has them first.
const oldestUpgradable = 28

//go:embed schema.sql
var schemaSQL string

// migrate brings the database to schemaVersion, or refuses to touch it.
//
//   - Empty: schema.sql, and schemaVersion recorded, in one transaction.
//   - At schemaVersion: nothing.
//   - From oldestUpgradable up: each step in migrations/ after the recorded
//     version, NNN_*.sql taking the database to version NNN, each in its own
//     transaction, which a foreign key it left dangling rolls back. Open then
//     hands back the space the rebuild freed (reclaim).
//   - Anything else — older than oldestUpgradable, newer than this binary, or
//     tables with no version at all — is refused with the reason, untouched.
func migrate(ctx context.Context, db *sql.DB) error {
	version, empty, err := schemaVersionOf(ctx, db)
	if err != nil {
		return err
	}
	switch {
	case empty:
		return createSchema(ctx, db)
	case version == schemaVersion:
		return nil
	case version > schemaVersion:
		return fmt.Errorf("the database is at schema version %d, newer than this release's %d: "+
			"run the release that wrote it, or a later one", version, schemaVersion)
	case version < oldestUpgradable:
		return fmt.Errorf("the database is at schema version %d, older than this release can upgrade "+
			"(it starts from %d): upgrade through a release that has migration %03d first, then this one",
			version, oldestUpgradable, oldestUpgradable)
	}

	steps, err := upgradeSteps(version)
	if err != nil {
		return err
	}
	for _, st := range steps {
		if err := applyStep(ctx, db, st); err != nil {
			return err
		}
	}
	return nil
}

// schemaVersionOf reads the recorded version. empty is a database with no
// tables at all, the one state migrate creates a schema in.
func schemaVersionOf(ctx context.Context, db *sql.DB) (version int, empty bool, err error) {
	var tables, versioned int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(name = 'schema_migrations'), 0) FROM sqlite_master
		  WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&tables, &versioned); err != nil {
		return 0, false, fmt.Errorf("read schema: %w", err)
	}
	if tables == 0 {
		return 0, true, nil
	}
	if versioned == 0 {
		return 0, false, errors.New("the database has tables but no schema_migrations: " +
			"it is not a claudication database, or not one this release can read")
	}
	var v sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, false, fmt.Errorf("read schema version: %w", err)
	}
	return int(v.Int64), false, nil
}

func createSchema(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	if err := recordVersion(ctx, tx, schemaVersion); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	return nil
}

func recordVersion(ctx context.Context, tx *sql.Tx, version int) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		version, time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("record schema version %d: %w", version, err)
	}
	return nil
}

type upgradeStep struct {
	version int
	name    string
	body    string
}

// upgradeSteps are the steps after from, in order. Every version from from+1
// to schemaVersion must have one: a gap would leave a database recorded at a
// version whose tables it does not have.
func upgradeSteps(from int) ([]upgradeStep, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	byVersion := map[int]upgradeStep{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		v, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return nil, fmt.Errorf("migration %s: filename must start with a version number", name)
		}
		body, err := migrationFS.ReadFile(path.Join("migrations", name))
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		byVersion[v] = upgradeStep{version: v, name: name, body: string(body)}
	}
	var steps []upgradeStep
	for v := from + 1; v <= schemaVersion; v++ {
		st, ok := byVersion[v]
		if !ok {
			return nil, fmt.Errorf("no migration to schema version %d", v)
		}
		steps = append(steps, st)
	}
	return steps, nil
}

// applyStep runs one step and records it, all or nothing. The foreign key
// check is inside the transaction because a rebuild that dropped a parent row
// a child still names would otherwise commit, and every later write to that
// child would fail.
func applyStep(ctx context.Context, db *sql.DB, st upgradeStep) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", st.name, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, st.body); err != nil {
		return fmt.Errorf("apply migration %s: %w", st.name, err)
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("check migration %s: %w", st.name, err)
	}
	dangling := rows.Next()
	var table string
	if dangling {
		_ = rows.Scan(&table)
	}
	rows.Close()
	if dangling {
		return fmt.Errorf("migration %s left a foreign key dangling in %s; nothing was changed", st.name, table)
	}
	if err := recordVersion(ctx, tx, st.version); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", st.name, err)
	}
	return nil
}

// reclaim hands back what an upgrade freed, on every open, before the gateway
// serves. A rebuild copies every table, so the file holds the old and the new
// until this runs. On a file that is already incremental with nothing free it
// costs two pragmas.
//
// VACUUM when the file is not in incremental auto-vacuum: it is the only thing
// that applies the mode the DSN asks for to a file created before it, and that
// file would otherwise keep its rebuilt size for ever. An incremental file
// gives its free pages back without a rewrite. Then the WAL, which holds a
// copy of every page the rebuild wrote, is checkpointed and truncated.
//
// A failure here is reported (CompactErr), not fatal: the upgrade is committed
// and the data current, and refusing to start would make an installer roll
// back to a binary that cannot read the new schema. It runs on every open
// rather than only the one that upgraded, so a VACUUM that failed — it wants
// about the database's size again free — is retried by the next start instead
// of leaving the file without auto-vacuum for good.
func reclaim(ctx context.Context, db *sql.DB) error {
	// One connection throughout, so VACUUM is not raced by another statement
	// and the checkpoint sees what it wrote.
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reclaim space: %w", err)
	}
	defer conn.Close()
	var mode int
	if err := conn.QueryRowContext(ctx, `PRAGMA auto_vacuum`).Scan(&mode); err != nil {
		return fmt.Errorf("reclaim space: %w", err)
	}
	if mode != 2 {
		_, err = conn.ExecContext(ctx, `VACUUM`)
	} else {
		_, err = conn.ExecContext(ctx, `PRAGMA incremental_vacuum`)
	}
	if err != nil {
		return fmt.Errorf("reclaiming the space the upgrade freed failed "+
			"(serving anyway; the next start retries): %w", err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("checkpoint: %w", err)
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// inTx runs fn inside one transaction: committed when fn returns nil, rolled
// back otherwise. what names the operation for the begin and commit errors;
// fn wraps its own.
//
// It exists so a write that touches more than one table cannot be left
// half-done by a failure between its statements — a key gone with its model
// roles kept, an admin gone with its sign-in links still valid.
func (s *Store) inTx(ctx context.Context, what string, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%s: begin: %w", what, err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%s: commit: %w", what, err)
	}
	return nil
}

// DB exposes the handle for packages that need direct access.
func (s *Store) DB() *sql.DB { return s.db }
