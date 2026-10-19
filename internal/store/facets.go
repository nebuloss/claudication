package store

import (
	"context"
	"fmt"
	"strconv"
)

// FacetValue is one line of a column's filter menu: what to narrow to, what to
// show for it, and how many requests carry it.
type FacetValue struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
	Count int64  `json:"count"`
}

// RequestFacets is what each filterable column can be narrowed to.
type RequestFacets struct {
	Models   []FacetValue `json:"models"`
	Clients  []FacetValue `json:"clients"`
	IPs      []FacetValue `json:"ips"`
	Statuses []FacetValue `json:"statuses"`
	Chats    []FacetValue `json:"chats"`
	Messages []FacetValue `json:"messages"`
	Kinds    []FacetValue `json:"kinds"`
	// Keys and Accounts are ids labelled with the name each has now, so a
	// filter arriving from a key's or an account's page reads as that key or
	// account rather than as a hex id.
	Keys     []FacetValue `json:"keys"`
	Accounts []FacetValue `json:"accounts"`
}

// RequestFacets lists what every filterable column holds, with counts.
//
// Read from the whole table rather than from the page on screen. The screen
// holds fifty rows, which is about seven minutes of a busy gateway, and a menu
// built from those can only ever offer what you are already looking at — the
// exact defect that made the old model picker useless. The log searches all of
// history and so does this.
//
// Deliberately not narrowed by the filter in force either. Counts that respond
// to the other filters look clever and take away the only way back: tick a
// model, and every value that model never used disappears from the other
// menus, so the filter that trapped you is the one you can no longer widen.
// These are totals, and the UI says so.
//
// Capped per column, commonest first. Addresses are the unbounded one — a
// scanner can mint thousands in an afternoon — and a menu is not a place to
// render them all; what a cap loses is the long tail of one-request values,
// which the IP cell is clickable for.
func (s *Store) RequestFacets(ctx context.Context, limit int) (RequestFacets, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	var out RequestFacets
	var err error
	if out.Models, err = s.facet(ctx, "model", limit); err != nil {
		return RequestFacets{}, err
	}
	if out.Clients, err = s.facet(ctx, "client", limit); err != nil {
		return RequestFacets{}, err
	}
	if out.IPs, err = s.facet(ctx, "ip", limit); err != nil {
		return RequestFacets{}, err
	}
	if out.Statuses, err = s.statusFacet(ctx, limit); err != nil {
		return RequestFacets{}, err
	}
	if out.Chats, err = s.chatFacet(ctx, limit); err != nil {
		return RequestFacets{}, err
	}
	if out.Messages, err = s.facet(ctx, "error_code", limit); err != nil {
		return RequestFacets{}, err
	}
	if out.Kinds, err = s.kindFacet(ctx); err != nil {
		return RequestFacets{}, err
	}
	if out.Keys, err = s.namedFacet(ctx, "key_id", nameKey, limit); err != nil {
		return RequestFacets{}, err
	}
	if out.Accounts, err = s.namedFacet(ctx, "account_id", nameAccount, limit); err != nil {
		return RequestFacets{}, err
	}
	return out, nil
}

// namedFacet is an id column's values labelled with each one's name, looked up
// for the page of ids only. Both arguments are this package's own constants.
func (s *Store) namedFacet(ctx context.Context, idColumn, kind string, limit int) ([]FacetValue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, COALESCE(`+nameOf(kind, "g.id")+`, ''), n
		  FROM (SELECT `+idColumn+` AS id, COUNT(*) AS n
		          FROM usage_events
		         WHERE `+idColumn+` <> ''
		         GROUP BY `+idColumn+`
		         ORDER BY COUNT(*) DESC, `+idColumn+` ASC
		         LIMIT ?) g
		 ORDER BY n DESC, id ASC`, limit)
	if err != nil {
		return nil, fmt.Errorf("facet %s: %w", idColumn, err)
	}
	defer rows.Close()

	out := []FacetValue{}
	for rows.Next() {
		var v FacetValue
		if err := rows.Scan(&v.Value, &v.Label, &v.Count); err != nil {
			return nil, fmt.Errorf("facet %s: %w", idColumn, err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// facet counts one column whose value is also its label.
//
// The column name is a constant from the caller, never anything a request
// carries: it is the one part of the statement that cannot be a parameter.
func (s *Store) facet(ctx context.Context, column string, limit int) ([]FacetValue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+column+`, COUNT(*) FROM usage_events
		GROUP BY `+column+`
		ORDER BY COUNT(*) DESC, `+column+` ASC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("facet %s: %w", column, err)
	}
	defer rows.Close()

	out := []FacetValue{}
	for rows.Next() {
		var v FacetValue
		if err := rows.Scan(&v.Value, &v.Count); err != nil {
			return nil, fmt.Errorf("facet %s: %w", column, err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// kindFacet counts what did and did not reach the upstream.
//
// Two rows at most, and it speaks the filter's vocabulary rather than the
// column's storage: the table holds three kinds, the filter says "relayed" or
// "rejected", and translating here keeps that word in one place. The
// gateway's own requests reached the upstream, so they count as relayed.
func (s *Store) kindFacet(ctx context.Context) ([]FacetValue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT kind = 1 AS rejected, COUNT(*) FROM usage_events GROUP BY rejected ORDER BY COUNT(*) DESC`)
	if err != nil {
		return nil, fmt.Errorf("facet kinds: %w", err)
	}
	defer rows.Close()

	out := []FacetValue{}
	for rows.Next() {
		var rejected bool
		var count int64
		if err := rows.Scan(&rejected, &count); err != nil {
			return nil, fmt.Errorf("facet kinds: %w", err)
		}
		// The column is called Forwarded and answers yes or no, so the menu
		// does too. The stored values keep their own names — "relayed" and
		// "rejected" are what the filter and the URL have always said, and a
		// link written last week still works.
		v := FacetValue{Value: "relayed", Label: "yes", Count: count}
		if rejected {
			v = FacetValue{Value: "rejected", Label: "no", Count: count}
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// chatFacet counts by conversation, labelled with the name the chat has if it
// has one.
//
// Its own query rather than facet("conversation_id") because an id is not
// something anyone recognises: a menu of sixteen hex characters is a menu you
// cannot use. The title comes from the same table the Chats screen reads, and
// a conversation without one keeps its id as the label.
//
// The empty conversation is excluded. Every request whose client named no chat
// shares it, which on most gateways is the largest bucket and is not a chat.
func (s *Store) chatFacet(ctx context.Context, limit int) ([]FacetValue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.conversation_id, COALESCE(t.title, ''), COUNT(*)
		FROM usage_events e
		LEFT JOIN chat_titles t ON t.conversation_id = e.conversation_id
		WHERE e.conversation_id <> ''
		GROUP BY e.conversation_id
		ORDER BY COUNT(*) DESC, e.conversation_id ASC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("facet chats: %w", err)
	}
	defer rows.Close()

	out := []FacetValue{}
	for rows.Next() {
		var v FacetValue
		var title string
		if err := rows.Scan(&v.Value, &title, &v.Count); err != nil {
			return nil, fmt.Errorf("facet chats: %w", err)
		}
		v.Label = title
		out = append(out, v)
	}
	return out, rows.Err()
}

// statusFacet counts by response code, with 0 named for what it means.
func (s *Store) statusFacet(ctx context.Context, limit int) ([]FacetValue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT status, COUNT(*) FROM usage_events
		GROUP BY status
		ORDER BY COUNT(*) DESC, status ASC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("facet statuses: %w", err)
	}
	defer rows.Close()

	out := []FacetValue{}
	for rows.Next() {
		var code int
		var count int64
		if err := rows.Scan(&code, &count); err != nil {
			return nil, fmt.Errorf("facet statuses: %w", err)
		}
		// 0 is not a status any upstream sent; it is this gateway recording
		// that nothing came back at all.
		label := strconv.Itoa(code)
		if code == 0 {
			label = "no answer"
		}
		out = append(out, FacetValue{Value: strconv.Itoa(code), Label: label, Count: count})
	}
	return out, rows.Err()
}
