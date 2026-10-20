package store

import (
	"context"
	"fmt"
	"time"
)

// SlotUse is what one client sent under one of its model names, served by
// one model, over a window: the row both the client's Usage page (one client,
// by slot) and an account's (by client, then slot) are made of, so the two
// views add up to the same figures.
type SlotUse struct {
	KeyID string `json:"client_id"`
	// Name is the client's name now (the names table), Type its type, ""
	// for a client since deleted.
	Name string `json:"client"`
	Type string `json:"type"`
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

// SlotScope narrows SlotUsage to one client or one account; exactly one is
// set.
type SlotScope struct {
	KeyID, AccountID string
}

// SlotUsage is the traffic since a moment, by client, model name and the
// model that served it, busiest first. Everything that reached an upstream: a
// refusal spent nothing and has no model. A client's own naming requests are
// its spend and are in it; the gateway's own titling is under its own key,
// which no client holds, and shows on an account's page as that key.
func (s *Store) SlotUsage(ctx context.Context, scope SlotScope, since time.Time) ([]SlotUse, error) {
	column, id := "key_id", scope.KeyID
	if scope.AccountID != "" {
		column, id = "account_id", scope.AccountID
	}
	if id == "" {
		return nil, fmt.Errorf("slot usage: no client or account named")
	}
	// column is one of two constants above, never anything a request carries.
	rows, err := s.db.QueryContext(ctx,
		`SELECT key_id, COALESCE(`+nameOf(nameKey, "u.key_id")+`, ''),
		        COALESCE((SELECT type FROM clients c WHERE c.key_id = u.key_id), ''),
		        alias, model, provider, COUNT(*), COALESCE(SUM(`+sqlIsError+`), 0),
		        COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(`+sqlCacheTokens+`), 0), COALESCE(SUM(reasoning_tokens), 0), MAX(at)
		   FROM usage_events u
		  WHERE kind <> 1 AND at >= ? AND `+column+` = ?
		  GROUP BY key_id, alias, model, provider
		  ORDER BY COUNT(*) DESC, key_id, alias, model`, ms(since), id)
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
		if err := rows.Scan(&u.KeyID, &u.Name, &u.Type, &u.Slot, &u.Model, &u.Provider, &u.Requests, &u.Errors,
			&u.InputTokens, &u.OutputTokens, &u.CacheTokens, &u.ReasoningTokens, &last); err != nil {
			return nil, fmt.Errorf("slot usage: %w", err)
		}
		u.Last = fromMS(last)
		out = append(out, u)
	}
	return out, rows.Err()
}
