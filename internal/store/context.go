package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// A chat's context is what its client resends on every turn: the prompt of
// each request, which with prompt caching is input + cache reads + cache
// writes. It grows turn by turn until the client compacts — summarises the
// conversation and starts over from the summary — and that is the only point
// at which it falls.
//
// Which is worth watching because the client decides it alone, from a context
// window in its own configuration, and the cost of deciding late is paid on
// every turn: the whole context is read again each time. The gateway cannot
// make a client compact; it can show where each chat stands.
//
// The figures are read off the requests the gateway relayed, with two rules
// that keep them honest:
//
//   - Only one model counts: the one that carried the chat's largest prompt.
//     A chat is not one stream of requests. crush titles a session on its small
//     model and Claude Code runs subagents beside the main thread, and their
//     prompts are a fraction of the conversation's — counted with it, every
//     such call would read as the context collapsing.
//   - A compaction is that model's context falling below half of what its
//     previous request carried, from at least compactionFloor. A summary is a
//     small fraction of what it replaces; ordinary turns move by a tool result
//     at a time. The floor keeps the first few turns of a chat, where halving is
//     ordinary, from counting.
//
// The second rule can still be fooled by a subagent on the main model, which
// Claude Code does run; the count is "compactions, probably", and the chart in
// the admin UI is there so a person can see which.

// compactionFloor is the smallest context whose halving counts as compacting.
const compactionFloor = 50_000

// ChatContext is where one chat's context stands.
type ChatContext struct {
	// Model is the one whose requests are counted; see above.
	Model string `json:"model"`
	// Now is the prompt of that model's latest successful request.
	Now int64 `json:"now"`
	// Peak is the largest prompt it carried.
	Peak int64 `json:"peak"`
	// Compactions is how many times it fell.
	Compactions int `json:"compactions"`
	// CompactedAt is when, oldest first; filled in for one chat's detail only.
	CompactedAt []time.Time `json:"compacted_at,omitempty"`
}

// PromptTokens is a request's context, by the rule above.
func (e UsageEvent) PromptTokens() int64 {
	return int64(e.InputTokens) + int64(e.CacheReadTokens) + int64(e.CacheWriteTokens)
}

// contextTracker reads one chat's requests, oldest first, and keeps the
// figures for every model in it until it is known which one counts.
type contextTracker struct {
	models map[string]*modelContext
	order  []string
}

type modelContext struct {
	now, peak   int64
	compactions int
	at          []time.Time
}

func (t *contextTracker) add(model string, at time.Time, prompt int64, keepTimes bool) {
	if model == "" || prompt <= 0 {
		return
	}
	if t.models == nil {
		t.models = map[string]*modelContext{}
	}
	m, ok := t.models[model]
	if !ok {
		m = &modelContext{}
		t.models[model] = m
		t.order = append(t.order, model)
	}
	if m.now >= compactionFloor && prompt*2 < m.now {
		m.compactions++
		if keepTimes {
			m.at = append(m.at, at)
		}
	}
	m.now = prompt
	m.peak = max(m.peak, prompt)
}

// result is the counted model's figures. Ties go to the model seen first, so
// the answer does not depend on map order.
func (t *contextTracker) result() ChatContext {
	var best string
	for _, name := range t.order {
		if best == "" || t.models[name].peak > t.models[best].peak {
			best = name
		}
	}
	if best == "" {
		return ChatContext{}
	}
	m := t.models[best]
	return ChatContext{Model: best, Now: m.now, Peak: m.peak, Compactions: m.compactions, CompactedAt: m.at}
}

// counts reports whether a request says anything about the conversation's
// context: answered, and relayed for the client rather than made by the
// gateway on its own behalf.
func counts(e UsageEvent) bool {
	return e.Status == 200 && e.Path != InternalPath && !e.Rejected
}

// ContextOf works out a chat's context from its requests, oldest first, with
// the times it compacted.
func ContextOf(events []UsageEvent) ChatContext {
	var t contextTracker
	for _, e := range events {
		if counts(e) {
			t.add(e.Model, e.At, e.PromptTokens(), true)
		}
	}
	return t.result()
}

// ChatContexts works out the context of each listed chat over the requests
// since a point in time, in one pass over them.
func (s *Store) ChatContexts(ctx context.Context, ids []string, since time.Time) (map[string]ChatContext, error) {
	out := make(map[string]ChatContext, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(ids)+2)
	args = append(args, stamp(since), InternalPath)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT conversation_id, model, at,
		        input_tokens + cache_read_tokens + cache_write_tokens
		   FROM usage_events
		  WHERE rejected = 0 AND status = 200 AND at >= ? AND path <> ?
		    AND conversation_id IN (?`+strings.Repeat(",?", len(ids)-1)+`)
		  ORDER BY conversation_id, at, id`, args...)
	if err != nil {
		return nil, fmt.Errorf("chat contexts: %w", err)
	}
	defer rows.Close()

	var (
		current string
		t       contextTracker
	)
	flush := func() {
		if current != "" {
			out[current] = t.result()
		}
	}
	for rows.Next() {
		var (
			id, model, at string
			prompt        int64
		)
		if err := rows.Scan(&id, &model, &at, &prompt); err != nil {
			return nil, fmt.Errorf("chat contexts: %w", err)
		}
		if id != current {
			flush()
			current, t = id, contextTracker{}
		}
		when, _ := time.Parse(time.RFC3339Nano, at)
		t.add(model, when, prompt, false)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("chat contexts: %w", err)
	}
	flush()
	return out, nil
}
