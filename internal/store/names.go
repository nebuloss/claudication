package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"claudication/internal/live"
)

// What keys and accounts are called, kept apart from both so history can be
// labelled after either is gone. See the names table in schema.sql.
//
// Written where a name is set — a key created or renamed, an account added,
// named, or reconnected under a new address — never on the request path. Read
// with every event, chat and breakdown, so a renamed key shows its current
// name across all of its history. An account is called its name where the
// operator gave it one and its address otherwise (Account.DisplayName).

// The kinds of name, as the table's CHECK spells them.
const (
	nameKey     = "key"
	nameAccount = "account"
)

// MaxNameLen is the most characters a client's or an account's name may have.
// A name is a label in a header, a table cell and a menu; past this it is a
// paragraph, and every one of those places would have to cut it.
const MaxNameLen = 100

// ErrInvalidName is a name that cannot be stored. Its text says why and is
// meant for the operator.
var ErrInvalidName = errors.New("invalid name")

type invalidName string

func (e invalidName) Error() string   { return string(e) }
func (e invalidName) Is(t error) bool { return t == ErrInvalidName }

// CleanName is the one rule a client's and an account's name follow, so the
// two read alike wherever they are shown: trimmed, at most MaxNameLen
// characters, and on one line — a control character would break the request
// log's export and every table row it landed in. Empty comes back empty;
// whether that is allowed is the caller's to say (a client needs a name, an
// account's empty name is its address).
func CleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) {
		return "", invalidName("a name must be text")
	}
	if n := utf8.RuneCountInString(name); n > MaxNameLen {
		return "", invalidName(fmt.Sprintf("a name can have at most %d characters (this one has %d)", MaxNameLen, n))
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", invalidName("a name must be on one line, without control characters")
	}
	return name, nil
}

// rememberAccountName records what an account is called now, read off its
// row so that every writer — sign-in, reconnect, naming — agrees on the rule.
func rememberAccountName(ctx context.Context, tx *sql.Tx, id string) error {
	//live:none part of its caller's write, which marks the topic
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO names (kind, id, name)
		 SELECT 'account', id, COALESCE(NULLIF(name, ''), email) FROM accounts WHERE id = ?
		 ON CONFLICT (kind, id) DO UPDATE SET name = excluded.name`, id); err != nil {
		return fmt.Errorf("remember account name: %w", err)
	}
	return nil
}

// execer is a *sql.DB or a *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// rememberName records what an id is called now.
func rememberName(ctx context.Context, db execer, kind, id, name string) error {
	//live:none part of its caller's write, which marks the topic
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
	defer s.changed(live.Requests)
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
