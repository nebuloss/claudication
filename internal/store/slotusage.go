package store

import (
	"context"
	"fmt"
	"time"
)

// SlotUse is what one client sent under one of its model names, served by
// one model, over a window: the row both the client's Usage page (one client,
// by slot) and an account's (by client, then slot) are made of, so the two
// views add up to the same figures. Activity's tables are the same rows read
// for the whole gateway, split by account as well.
type SlotUse struct {
	KeyID string `json:"client_id"`
	// Name is the client's name now (the names table), Type its type, ""
	// for a client since deleted.
	Name string `json:"client"`
	Type string `json:"type"`
	// AccountID and Account are the account that served it and what it is
	// called now, on the gateway-wide rows only: a scoped read is either one
	// account's already, or a client's added up over its accounts.
	AccountID string `json:"account_id,omitempty"`
	Account   string `json:"account,omitempty"`
	// Slot is the model name it asked for, as recorded (usage_events.alias);
	// empty for a direct-mode request naming a real model.
	Slot     string `json:"slot"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Requests int64  `json:"requests"`
	Errors   int64  `json:"errors"`
	// The tokens by kind; ReasoningTokens is inside OutputTokens.
	InputTokens     int64     `json:"input_tokens"`
	OutputTokens    int64     `json:"output_tokens"`
	CacheTokens     int64     `json:"cache_tokens"`
	ReasoningTokens int64     `json:"reasoning_tokens"`
	Last            time.Time `json:"last"`
}

// SlotScope narrows SlotUsage to one client or one account, or with Gateway
// reads every event and splits the rows by account too; exactly one is set.
// Gateway is a field of its own rather than the zero value, so a scope whose
// id went missing on the way is an error, not the whole gateway's traffic on
// one client's page.
type SlotScope struct {
	KeyID, AccountID string
	Gateway          bool
}

// SlotUsage is the traffic since a moment, by client, model name and the
// model that served it, busiest first. Everything that reached an upstream: a
// refusal spent nothing and has no model. A client's own naming requests are
// its spend and are in it; the gateway's own titling is under its own key,
// which no client holds, and shows on an account's page as that key.
//
// The gateway-wide read is one grouped pass that every Activity table is
// folded from — by model, by client, by account — rather than a query per
// table: they are three ways of adding up the same rows, and folding them
// from one set is what makes a model's clients add up to the model.
func (s *Store) SlotUsage(ctx context.Context, scope SlotScope, since time.Time) ([]SlotUse, error) {
	// where and account are fixed strings chosen here, never anything a
	// request carries.
	where, account := "", `''`
	var args []any
	switch {
	case scope.Gateway:
		account = "account_id"
	case scope.AccountID != "":
		where, args = " AND account_id = ?", []any{scope.AccountID}
	case scope.KeyID != "":
		where, args = " AND key_id = ?", []any{scope.KeyID}
	default:
		return nil, fmt.Errorf("slot usage: no client or account named")
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT key_id, COALESCE(`+nameOf(nameKey, "u.key_id")+`, ''),
		        COALESCE((SELECT type FROM clients c WHERE c.key_id = u.key_id), ''),
		        `+account+`, COALESCE(`+nameOf(nameAccount, account)+`, ''),
		        alias, model, provider, COUNT(*), COALESCE(SUM(`+sqlIsError+`), 0),
		        COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(`+sqlCacheTokens+`), 0), COALESCE(SUM(reasoning_tokens), 0), MAX(at)
		   FROM usage_events u
		  WHERE kind IN (0, 2) AND at >= ?`+where+`
		  GROUP BY key_id, `+account+`, alias, model, provider
		  ORDER BY COUNT(*) DESC, key_id, `+account+`, alias, model`, append([]any{ms(since)}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("slot usage: %w", err)
	}
	defer rows.Close()
	out := []SlotUse{}
	for rows.Next() {
		var (
			u    SlotUse
			last int64
		)
		if err := rows.Scan(&u.KeyID, &u.Name, &u.Type, &u.AccountID, &u.Account, &u.Slot, &u.Model, &u.Provider,
			&u.Requests, &u.Errors, &u.InputTokens, &u.OutputTokens, &u.CacheTokens, &u.ReasoningTokens, &last); err != nil {
			return nil, fmt.Errorf("slot usage: %w", err)
		}
		u.Last = fromMS(last)
		out = append(out, u)
	}
	return out, rows.Err()
}
