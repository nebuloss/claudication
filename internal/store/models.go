package store

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Which models an account may serve.
//
// An operator can turn a model off for an account: a work subscription kept
// off the expensive model, a personal one kept for it, a model withdrawn from
// a whole gateway by turning it off everywhere. The pool then serves a request
// only from an account that has its model on, and refuses it outright when no
// account does.
//
// Kept as what is off rather than what is on. A list of what is on would turn
// every model Anthropic releases into one nobody can use until somebody
// notices and switches it on, and a request naming a model the gateway has
// never seen listed — an alias, a model released this morning — would have to
// be refused for not being on a list. Off is a decision someone made; on is
// the default, as it is for everything else this gateway can switch.

// dated is the release date some ids end in.
var dated = regexp.MustCompile(`-\d{8}$`)

// ModelKey is what two spellings of one model have in common.
//
// Anthropic lists some models by a dated id and clients ask for them by the
// alias — claude-haiku-4-5-20251001 and claude-haiku-4-5 are the same model,
// and the request log has both. A switch has to cover both, or turning a model
// off would leave it reachable by its other name.
func ModelKey(model string) string {
	return dated.ReplaceAllString(strings.ToLower(strings.TrimSpace(model)), "")
}

// Serves reports whether the account has a model on. An empty model — a
// request that names none, or a caller that does not care which — is served.
func (a Account) Serves(model string) bool {
	if model == "" {
		return true
	}
	key := ModelKey(model)
	for _, off := range a.ModelsOff {
		if ModelKey(off) == key {
			return false
		}
	}
	return true
}

// SetAccountModel turns a model on or off for an account. Turning on an id
// that was turned off under its other spelling turns that row off too, so
// the switch the operator sees and the one that routes cannot disagree.
func (s *Store) SetAccountModel(ctx context.Context, id, model string, on bool) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return fmt.Errorf("set account model: no model")
	}
	acct, err := s.Account(ctx, id)
	if err != nil {
		return err
	}
	if on {
		for _, off := range acct.ModelsOff {
			if ModelKey(off) != ModelKey(model) {
				continue
			}
			if _, err := s.db.ExecContext(ctx,
				`DELETE FROM account_models_off WHERE account_id = ? AND model = ?`, id, off); err != nil {
				return fmt.Errorf("set account model: %w", err)
			}
		}
		return nil
	}
	if !acct.Serves(model) {
		return nil // already off, under this spelling or the other
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO account_models_off (account_id, model, off_at) VALUES (?, ?, ?)`,
		id, model, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("set account model: %w", err)
	}
	return nil
}
