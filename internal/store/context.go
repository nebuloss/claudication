package store

import (
	"context"
	"fmt"
	"slices"
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
//   - Only the main thread counts: the models that carried the conversation,
//     each with a largest prompt at least half the chat's largest. A chat is
//     not one stream of requests. crush titles a session on its small model
//     and Claude Code runs subagents beside the main thread, and their prompts
//     are a fraction of the conversation's — counted with it, every such call
//     would read as the context collapsing. A switch of model mid-chat is not
//     one of those: the new model is resent the same history, and carries on.
//   - A compaction is the main thread's context falling below half of what its
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
//
// It follows the chat's main thread, which is not always one model: a user
// can switch model mid-chat (Claude Code's /model), and the new model is
// resent the same history, so the context carries on rather than starting
// over. The main thread is every model whose largest prompt is at least half
// the chat's largest — a model that carried the conversation — and not the
// small ones beside it: a title asked of a fast model, a quick check. Its
// requests are read as one sequence, so a switch is not a compaction, and
// where the model changed is kept beside where it compacted.
type ChatContext struct {
	// Model is the main thread's model now: its latest request's.
	Model string `json:"model"`
	// MainModels are the models of the main thread, in the order they first
	// carried it; every other model in the chat ran beside it.
	MainModels []string `json:"main_models,omitempty"`
	// Now is the prompt of that model's latest successful request.
	Now int64 `json:"now"`
	// Peak is the largest prompt it carried.
	Peak int64 `json:"peak"`
	// Compactions is how many times it fell.
	Compactions int `json:"compactions"`
	// CompactedAt is when, oldest first; filled in for one chat's detail only.
	CompactedAt []time.Time `json:"compacted_at,omitempty"`
	// Switches is when the main thread changed model and to which, oldest
	// first; for one chat's detail only.
	Switches []ModelSwitch `json:"switches,omitempty"`
}

// ModelSwitch is the main thread moving to another model.
type ModelSwitch struct {
	At    time.Time `json:"at"`
	Model string    `json:"model"`
}

// PromptTokens is a request's context, by the rule above.
func (e UsageEvent) PromptTokens() int64 {
	return int64(e.InputTokens) + int64(e.CacheReadTokens) + int64(e.CacheWriteTokens)
}

// contextTracker reads one chat's requests, oldest first, and keeps them
// until it is known which models are the main thread: that takes the whole
// chat, since a model's peak decides it.
type contextTracker struct {
	seen []prompted
}

type prompted struct {
	model  string
	at     time.Time
	prompt int64
}

func (t *contextTracker) add(model string, at time.Time, prompt int64) {
	if model == "" || prompt <= 0 {
		return
	}
	t.seen = append(t.seen, prompted{model, at, prompt})
}

// result is the main thread's figures; keepTimes fills in when it compacted
// and switched, which only one chat's detail shows.
func (t *contextTracker) result(keepTimes bool) ChatContext {
	peaks := map[string]int64{}
	var top int64
	for _, p := range t.seen {
		peaks[p.model] = max(peaks[p.model], p.prompt)
		top = max(top, p.prompt)
	}
	if top == 0 {
		return ChatContext{}
	}
	var out ChatContext
	var now int64
	for _, p := range t.seen {
		if peaks[p.model]*2 < top {
			continue // beside the main thread
		}
		if out.Model != "" && p.model != out.Model {
			if keepTimes {
				out.Switches = append(out.Switches, ModelSwitch{At: p.at, Model: p.model})
			}
		}
		if !slices.Contains(out.MainModels, p.model) {
			out.MainModels = append(out.MainModels, p.model)
		}
		if now >= compactionFloor && p.prompt*2 < now {
			out.Compactions++
			if keepTimes {
				out.CompactedAt = append(out.CompactedAt, p.at)
			}
		}
		now = p.prompt
		out.Model = p.model
		out.Peak = max(out.Peak, p.prompt)
	}
	out.Now = now
	return out
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
			t.add(e.Model, e.At, e.PromptTokens())
		}
	}
	return t.result(true)
}

// ChatContexts works out the context of each listed chat over the requests
// since a point in time, in one pass over them.
func (s *Store) ChatContexts(ctx context.Context, ids []string, since time.Time) (map[string]ChatContext, error) {
	out := make(map[string]ChatContext, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(ids)+2)
	args = append(args, ms(since))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT conversation_id, model, at,
		        input_tokens + cache_read_tokens + cache_write_tokens
		   FROM usage_events
		  WHERE kind = 0 AND status = 200 AND at >= ?
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
			out[current] = t.result(false)
		}
	}
	for rows.Next() {
		var (
			id, model  string
			at, prompt int64
		)
		if err := rows.Scan(&id, &model, &at, &prompt); err != nil {
			return nil, fmt.Errorf("chat contexts: %w", err)
		}
		if id != current {
			flush()
			current, t = id, contextTracker{}
		}
		t.add(model, fromMS(at), prompt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("chat contexts: %w", err)
	}
	flush()
	return out, nil
}
