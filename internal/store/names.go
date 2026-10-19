package store

import (
	"context"
	"database/sql"
	"fmt"
)

// What keys and accounts are called, kept apart from both so history can be
// labelled after either is gone. See the names table in schema.sql.
//
// Written where a name is set — a key created or renamed, an account added or
// reconnected under a new address — never on the request path. Read with every
// event, chat and breakdown, so a renamed key shows its current name across
// all of its history.

// The kinds of name, as the table's CHECK spells them.
const (
	nameKey     = "key"
	nameAccount = "account"
)

// execer is a *sql.DB or a *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// rememberName records what an id is called now.
func rememberName(ctx context.Context, db execer, kind, id, name string) error {
	if id == "" || name == "" {
		return nil
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO names (kind, id, name) VALUES (?, ?, ?)
		 ON CONFLICT (kind, id) DO UPDATE SET name = excluded.name`, kind, id, name); err != nil {
		return fmt.Errorf("remember %s name: %w", kind, err)
	}
	return nil
}

// nameOf is the SQL for the name of the id idExpr evaluates to, or NULL. A
// scalar subquery rather than a join, so the statements it goes into keep
// their unqualified columns: names has a kind and an id of its own.
func nameOf(kind, idExpr string) string {
	return `(SELECT name FROM names WHERE kind = '` + kind + `' AND id = ` + idExpr + `)`
}

// PruneNames drops the names nothing refers to any more: no key or account by
// that id, and no event left that carries it. Run after the usage prune, which
// is what lets a deleted key's name go.
//
// NOT IN rather than NOT EXISTS: usage_events has no index on either id, and
// the uncorrelated form reads the table once rather than once per name.
func (s *Store) PruneNames(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM names
		 WHERE (kind = 'key'
		        AND id NOT IN (SELECT id FROM api_keys)
		        AND id NOT IN (SELECT key_id FROM usage_events))
		    OR (kind = 'account'
		        AND id NOT IN (SELECT id FROM accounts)
		        AND id NOT IN (SELECT account_id FROM usage_events))`)
	if err != nil {
		return 0, fmt.Errorf("prune names: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
