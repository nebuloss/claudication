package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// A worker started beside a serving one may share the database only as it is.
// Migrating it would change the schema under the other process, which goes on
// writing what the old one expects; the refusal is what makes the supervisor
// fall back to a stop and a start.
func TestOpenAlongsideNeverMigrates(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "claudication.db")
	sealer := testSealer(t)

	if _, err := OpenAlongside(ctx, path, sealer); !errors.Is(err, ErrSchemaChange) {
		t.Fatalf("empty database: err = %v, want ErrSchemaChange", err)
	}

	serving, err := Open(ctx, path, sealer)
	if err != nil {
		t.Fatal(err)
	}
	defer serving.Close()

	beside, err := OpenAlongside(ctx, path, sealer)
	if err != nil {
		t.Fatalf("current database: %v", err)
	}
	// Both write: WAL with a busy timeout is what makes the overlap safe.
	if err := beside.SetSetting(ctx, "k", "beside"); err != nil {
		t.Fatal(err)
	}
	if err := serving.SetSetting(ctx, "k", "serving"); err != nil {
		t.Fatal(err)
	}
	beside.Close()

	// A database one step behind, as the old release left it.
	if _, err := serving.db.ExecContext(ctx, `DELETE FROM schema_migrations`); err != nil {
		t.Fatal(err)
	}
	if _, err := serving.db.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, 0)`, schemaVersion-1); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAlongside(ctx, path, sealer); !errors.Is(err, ErrSchemaChange) {
		t.Fatalf("older database: err = %v, want ErrSchemaChange", err)
	}
	var v int
	if err := serving.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil || v != schemaVersion-1 {
		t.Errorf("version after the refusal = %d (%v), want it untouched at %d", v, err, schemaVersion-1)
	}
}
