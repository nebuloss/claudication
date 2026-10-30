package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"claudication/internal/live"
)

// Accents are the colours an account or a client can wear — the tile its mark
// sits on — in the order they are handed out: two accounts of one provider,
// or two clients of one type, wear the same mark, and this is what tells them
// apart. None is a status colour (green, amber, red), which mean something
// else on every screen. web/src/ui/brands/accents.ts names the same list, and
// a test holds the two together; migration 043 hands them out in this order.
var Accents = []string{"blue", "violet", "teal", "pink", "indigo", "cyan", "sand", "slate"}

// ErrInvalidAccent is a colour not in Accents.
var ErrInvalidAccent = errors.New("not one of the accent colours")

// nextAccent is the first colour of the palette none of used has, or — every
// one taken — the one fewest have, so a ninth account of one provider shares
// with as few as can be.
func nextAccent(used []string) string {
	count := map[string]int{}
	for _, a := range used {
		count[a]++
	}
	best := Accents[0]
	for _, a := range Accents {
		if count[a] == 0 {
			return a
		}
		if count[a] < count[best] {
			best = a
		}
	}
	return best
}

// accentsUsed are the colours the rows a query selects wear.
func accentsUsed(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetAccountAccent sets the colour an account wears.
func (s *Store) SetAccountAccent(ctx context.Context, id, accent string) error {
	defer s.changed(live.Accounts)
	if !slices.Contains(Accents, accent) {
		return ErrInvalidAccent
	}
	res, err := s.db.ExecContext(ctx, `UPDATE accounts SET accent = ? WHERE id = ?`, accent, id)
	if err != nil {
		return fmt.Errorf("colour account: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAccountNotFound
	}
	return nil
}

// SetClientAccent sets the colour a client wears.
func (s *Store) SetClientAccent(ctx context.Context, keyID, accent string) error {
	defer s.changed(live.Clients)
	if !slices.Contains(Accents, accent) {
		return ErrInvalidAccent
	}
	res, err := s.db.ExecContext(ctx, `UPDATE clients SET accent = ? WHERE key_id = ?`, accent, keyID)
	if err != nil {
		return fmt.Errorf("colour client: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrClientNotFound
	}
	return nil
}
