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
func (a Account) Serves(model string) bool { return modelOn(a.ModelsOff, model) }

// Allows reports whether a key may ask for a model, by the same rule.
func (k APIKey) Allows(model string) bool { return modelOn(k.ModelsOff, model) }

func modelOn(off []string, model string) bool {
	if model == "" {
		return true
	}
	key := ModelKey(model)
	for _, o := range off {
		if ModelKey(o) == key {
			return false
		}
	}
	return true
}

// SetKeyModel turns a model on or off for an API key, by the rule
// SetAccountModel follows: turning a model on clears it under either spelling.
func (s *Store) SetKeyModel(ctx context.Context, id, model string, on bool) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return fmt.Errorf("set key model: no model")
	}
	key, err := s.Key(ctx, id)
	if err != nil {
		return err
	}
	if on {
		for _, off := range key.ModelsOff {
			if ModelKey(off) != ModelKey(model) {
				continue
			}
			if _, err := s.db.ExecContext(ctx,
				`DELETE FROM api_key_models_off WHERE key_id = ? AND model = ?`, id, off); err != nil {
				return fmt.Errorf("set key model: %w", err)
			}
		}
		return nil
	}
	if !key.Allows(model) {
		return nil
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO api_key_models_off (key_id, model, off_at) VALUES (?, ?, ?)`,
		id, model, ms(time.Now()))
	if err != nil {
		return fmt.Errorf("set key model: %w", err)
	}
	return nil
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
		// Already off. Off by the gateway, it is the operator's now: they
		// chose it, and the gateway must not turn it back on.
		for _, off := range acct.ModelsOff {
			if ModelKey(off) == ModelKey(model) {
				if _, err := s.db.ExecContext(ctx,
					`UPDATE account_models_off SET by = 'operator' WHERE account_id = ? AND model = ?`, id, off); err != nil {
					return fmt.Errorf("set account model: %w", err)
				}
			}
		}
		return nil
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO account_models_off (account_id, model, off_at, by) VALUES (?, ?, ?, 'operator')`,
		id, model, ms(time.Now()))
	if err != nil {
		return fmt.Errorf("set account model: %w", err)
	}
	return nil
}

// SwitchModelOffAuto turns a model off for an account on the gateway's own
// account, as the operator's switch does, marked as the gateway's: what
// SwitchModelOnAuto may turn back on. False when it was off already.
func (s *Store) SwitchModelOffAuto(ctx context.Context, id, model string) (bool, error) {
	acct, err := s.Account(ctx, id)
	if err != nil {
		return false, err
	}
	if !acct.Serves(model) {
		return false, nil
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO account_models_off (account_id, model, off_at, by) VALUES (?, ?, ?, 'auto')`,
		id, model, ms(time.Now())); err != nil {
		return false, fmt.Errorf("switch model off: %w", err)
	}
	return true, nil
}

// SwitchModelOnAuto turns back on a model the gateway turned off for an
// account, under either spelling, and never one the operator did. False when
// there was none to turn on.
func (s *Store) SwitchModelOnAuto(ctx context.Context, id, model string) (bool, error) {
	acct, err := s.Account(ctx, id)
	if err != nil {
		return false, err
	}
	on := false
	for _, off := range acct.ModelsOff {
		if ModelKey(off) != ModelKey(model) {
			continue
		}
		res, err := s.db.ExecContext(ctx,
			`DELETE FROM account_models_off WHERE account_id = ? AND model = ? AND by = 'auto'`, id, off)
		if err != nil {
			return false, fmt.Errorf("switch model on: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			on = true
		}
	}
	return on, nil
}

// AutoOffModels are the models the gateway itself turned off for an account.
func (s *Store) AutoOffModels(ctx context.Context, id string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT model FROM account_models_off WHERE account_id = ? AND by = 'auto'`, id)
	if err != nil {
		return nil, fmt.Errorf("auto-off models: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, fmt.Errorf("auto-off model: %w", err)
		}
		out[m] = true
	}
	return out, rows.Err()
}
