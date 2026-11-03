package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/service/capture"
	"claudication/internal/store"
)

// reportWindow is the time window a usage or chat request asks for with
// ?days=, bounded by retention, or the configured default when it asks for
// none.
//
// One rule for both rather than two copies of it: a chat list over a
// different window than the charts beside it is a screen that appears to
// disagree with itself.
func (s *Admin) reportWindow(r *http.Request) time.Duration {
	days := httpx.QueryInt(r.URL.Query(), "days", 0, s.cfg.Usage.RetentionDays)
	if days == 0 {
		return s.cfg.Usage.Window()
	}
	return time.Duration(days) * 24 * time.Hour
}

// handleChats answers the chat list.
func (s *Admin) handleChats(w http.ResponseWriter, r *http.Request) {
	// Usage switched off means no events are being recorded at all, so there
	// is nothing to group. Saying so is better than an empty list, which reads
	// as "nobody has used this".
	if !s.cfg.Usage.Enabled() {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}

	window := s.reportWindow(r)
	limit := httpx.QueryInt(r.URL.Query(), "limit", 100, store.MaxChatsPage)

	report, err := s.store.ChatsIn(r.Context(), time.Now().Add(-window), limit, scopeFrom(r.URL.Query()))
	if err != nil {
		s.log.Error("chat list", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not read chats")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"enabled":        true,
		"days":           int(window.Hours() / 24),
		"retention_days": s.cfg.Usage.RetentionDays,
		"report":         report,
	})
}

// handleSetChatTitle stores a title the operator typed.
//
// It holds against every automatic rename (chat_titles.manual), so the one name
// known to be right is not replaced by a guess. Works with generation off,
// since nothing is spent.
func (s *Admin) handleSetChatTitle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Title string `json:"title"`
	}
	const usage = `expected {"title": "…"}`
	if !httpx.DecodeJSONLimit(w, r, &body, 1<<10, usage) {
		return
	}
	if id == "" {
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, usage)
		return
	}
	if strings.TrimSpace(body.Title) == "" {
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, "a title cannot be empty")
		return
	}
	title, err := s.store.SetManualTitle(r.Context(), id, body.Title)
	if err != nil {
		s.log.Error("could not store a chat title", "err", err, "chat", id)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not store the title")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"title": title, "title_manual": true})
}

// handleChat answers one conversation's requests.
func (s *Admin) handleChat(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Usage.Enabled() {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}

	id := r.PathValue("id")
	if id == "" {
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, "no conversation id")
		return
	}

	events, err := s.store.ChatEvents(r.Context(), id, 0)
	if err != nil {
		s.log.Error("chat events", "err", err, "chat", id)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not read the chat")
		return
	}
	// An id nobody recognises is a 404 rather than an empty list: a chat that
	// has been pruned and one that never existed are the same thing from here,
	// and both mean the link the operator followed is stale.
	if len(events) == 0 {
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such chat, or its events have been pruned")
		return
	}

	// The same shape the recent-requests list uses, through the same
	// conversion. store.UsageEvent carries no JSON tags, so returning it raw
	// would put Go field names on the wire and give this one endpoint a
	// vocabulary of its own.
	captured := s.capture.Recorded(r.Context(), eventIDs(events))
	titles, err := s.store.ChatTitles(r.Context(), []string{id})
	if err != nil {
		s.log.Warn("could not read the chat's title", "err", err, "chat", id)
	}
	out := make([]RequestJSON, 0, len(events))
	for _, e := range events {
		row := s.toRequestJSON(e)
		if captured[e.ID] {
			row.Capture = e.ID
		}
		row.ChatTitle = titles[id]
		out = append(out, row)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"enabled":  true,
		"id":       id,
		"requests": out,
		// Over the requests returned, with the times it compacted, so the
		// chart marks the same falls the list counts.
		"context": store.ContextOf(events),
	})
}

// A chat is one session as its client names it, and a session is not one
// conversation. Measured with Claude Code 2.1.317 through the gateway
// (2026-11-03): a session that launched a subagent sent the main thread's
// requests and the subagent's under the one X-Claude-Code-Session-Id, on the
// same model, interleaved in time — the main thread at 2 then 4 messages, the
// subagent at 2, 5, 7, 9, each from a first message and a system prompt of its
// own. Read as one sequence they are nonsense: the subagent's first request
// carried fewer messages than the main thread's before it.
//
// So a chat is read as threads. A thread is the requests whose first message
// is the same — the conversation's root, which every turn resends unchanged —
// read from the request in Messages, the form every API is translated
// through, with every cache marker taken out, since a client moves those
// between turns. Nothing in it is any client's: Claude Code marks a subagent's
// requests (cc_is_subagent in its billing header), and no other client does.
//
// The main thread is the one carrying the chat's largest prompt, as its
// context is (internal/store, ContextOf), and any other with its system prompt
// is the main thread too: a compaction replaces the conversation, root and
// all, under the same instructions. Every other thread ran beside it — a
// subagent, a title asked of a small model — and is named as one beside it,
// in the order they began.

// ThreadJSON is one thread of a chat.
type ThreadJSON struct {
	// ID numbers it within the chat: 1 is the main thread's first, the rest
	// in the order they began.
	ID    int    `json:"id"`
	Label string `json:"label"`
	Main  bool   `json:"main"`
	// Requests is how many of the chat's recorded requests are on it.
	Requests int `json:"requests"`
}

// TurnJSON is what one recorded request of a chat added to it: the messages
// new since the turn before it on its thread, and the answer.
type TurnJSON struct {
	// Event is the request's row, and Capture its recording: the same id,
	// since a recording is known by its row's.
	Event   int64 `json:"event"`
	Capture int64 `json:"capture"`
	// Thread is the thread it is on (ThreadJSON.ID), 0 when it could not be
	// read.
	Thread int `json:"thread"`
	// Offset is how many messages came before the added ones, which numbers
	// them as the conversation does.
	Offset int `json:"offset"`
	// Restarted is a request that carried fewer messages than the turn
	// before it on its thread. Its messages are all shown.
	Restarted bool              `json:"restarted,omitempty"`
	Added     []json.RawMessage `json:"added"`
	Answer    json.RawMessage   `json:"answer,omitempty"`
	// Unread is why the request could not be read as a conversation.
	Unread string `json:"unread,omitempty"`
}

// handleChatTranscript answers a chat's threads, which thread each of its
// recorded requests is on, and what each added to its thread, oldest first:
// the chat read as transcripts, from the recordings held or stored
// (internal/service/capture). ?from= and ?to= (request log row ids, inclusive)
// narrow the turns to the window a page is showing; the threads, and the
// turns before the window, are still read, since what a turn added depends
// on them. ?threads=only answers the threads alone.
//
// A client resends the whole conversation every turn, so a request's own
// body is mostly the turns before it. What is new is what follows the turn
// before it on its thread, its answer included — counted, not compared: Claude
// Code moves its cache markers between turns, so the same message is not the
// same bytes twice, and the count is what holds.
func (s *Admin) handleChatTranscript(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := r.URL.Query()
	from, _ := strconv.ParseInt(q.Get("from"), 10, 64)
	to, _ := strconv.ParseInt(q.Get("to"), 10, 64)
	only := q.Get("threads") == "only"
	within := func(event int64) bool {
		return !only && (from == 0 || event >= from) && (to == 0 || event <= to)
	}

	events, err := s.store.ChatEvents(r.Context(), id, 0)
	if err != nil {
		s.log.Error("chat events", "err", err, "chat", id)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not read the chat")
		return
	}
	captured := s.capture.Recorded(r.Context(), eventIDs(events))
	// A recording, held or read back from disk; one that cannot be read is
	// left out, as one that was never made is.
	recording := func(event int64) *capture.Exchange {
		x, err := s.capture.ByEvent(r.Context(), event)
		if err != nil {
			if !errors.Is(err, capture.ErrNotRecorded) {
				s.log.Warn("could not read a recording", "event", event, "err", err)
			}
			return nil
		}
		return x
	}

	// First, every recorded request read for its thread and count: from the
	// cache, or its recording.
	type read struct {
		event    store.UsageEvent
		count    turnCount
		messages []json.RawMessage
		answer   json.RawMessage
	}
	var all []read
	for _, e := range events {
		if !captured[e.ID] {
			continue
		}
		rd := read{event: e}
		if within(e.ID) {
			x := recording(e.ID)
			if x == nil {
				continue
			}
			l := s.legJSON(&x.Client)
			rd.messages, rd.count = readTurn(l)
			if rd.count.unread == "" && l.Response != nil && l.Response.Messages != nil {
				rd.answer = l.Response.Messages
			}
			s.counts.put(e.ID, rd.count)
		} else if c, ok := s.counts.get(e.ID); ok {
			rd.count = c
		} else {
			x := recording(e.ID)
			if x == nil {
				continue
			}
			_, rd.count = readTurn(s.legJSON(&x.Client))
			s.counts.put(e.ID, rd.count)
		}
		all = append(all, rd)
	}

	// Then the threads: by root, the main one by its largest prompt and its
	// system prompt.
	type thread struct {
		id       int
		system   string
		peak     int64
		requests int
	}
	byRoot := map[string]*thread{}
	var order []*thread
	for _, rd := range all {
		if rd.count.unread != "" {
			continue
		}
		t, ok := byRoot[rd.count.root]
		if !ok {
			t = &thread{system: rd.count.system}
			byRoot[rd.count.root] = t
			order = append(order, t)
		}
		t.requests++
		t.peak = max(t.peak, rd.event.PromptTokens())
	}
	var main *thread
	for _, t := range order {
		if main == nil || t.peak > main.peak {
			main = t
		}
	}
	threads := []ThreadJSON{}
	side := 0
	// The main thread's first, then the rest as they began.
	numbered := func(t *thread, isMain bool) {
		t.id = len(threads) + 1
		label := "Main"
		if !isMain {
			side++
			label = "Side thread " + strconv.Itoa(side)
		} else if len(threads) > 0 {
			label = "Main, continued"
		}
		threads = append(threads, ThreadJSON{ID: t.id, Label: label, Main: isMain, Requests: t.requests})
	}
	isMain := func(t *thread) bool { return main != nil && (t == main || (t.system != "" && t.system == main.system)) }
	for _, t := range order {
		if isMain(t) {
			numbered(t, true)
		}
	}
	for _, t := range order {
		if !isMain(t) {
			numbered(t, false)
		}
	}

	// Last, each turn against the one before it on its thread.
	seen := map[int]int{}
	turns := []TurnJSON{}
	for _, rd := range all {
		t := TurnJSON{Event: rd.event.ID, Capture: rd.event.ID, Added: []json.RawMessage{}}
		if rd.count.unread != "" {
			t.Unread = rd.count.unread
			if within(rd.event.ID) {
				turns = append(turns, t)
			}
			continue
		}
		t.Thread = byRoot[rd.count.root].id
		if before, ok := seen[t.Thread]; ok {
			if rd.count.messages >= before {
				t.Offset = before
			} else {
				t.Restarted = true
			}
		}
		next := rd.count.messages
		if rd.count.answered {
			next++
		}
		seen[t.Thread] = next
		if within(rd.event.ID) {
			t.Added = rd.messages[t.Offset:]
			t.Answer = rd.answer
			turns = append(turns, t)
		}
	}
	out := map[string]any{"id": id, "threads": threads}
	if only {
		// Which thread each recorded request is on, for a list of them to say.
		of := map[string]int{}
		for _, rd := range all {
			if th, ok := byRoot[rd.count.root]; ok && rd.count.unread == "" {
				of[strconv.FormatInt(rd.event.ID, 10)] = th.id
			}
		}
		out["thread_of"] = of
	} else {
		out["turns"] = turns
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// turnCount is what the transcript needs of a turn outside its window: how
// many messages it carried, whether it was answered, its thread's root and
// system prompt, or why it could not be read.
type turnCount struct {
	messages int
	answered bool
	unread   string
	// root and system identify its thread: hashes of its first message and
	// its system prompt, cache markers taken out.
	root, system string
}

// readTurn reads a client's leg as one turn: its messages, and its count.
func readTurn(l LegJSON) ([]json.RawMessage, turnCount) {
	if l.Request.Messages == nil {
		return nil, turnCount{unread: l.Request.MessagesError}
	}
	var req struct {
		System   json.RawMessage   `json:"system"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(l.Request.Messages, &req); err != nil {
		return nil, turnCount{unread: err.Error()}
	}
	c := turnCount{
		messages: len(req.Messages),
		answered: l.Response != nil && l.Response.Messages != nil,
		system:   partHash(req.System),
	}
	if len(req.Messages) > 0 {
		c.root = partHash(req.Messages[0])
	}
	return req.Messages, c
}

// partHash is a hash of a part of a request with its cache markers taken
// out: the same text resent with its marker moved is the same part.
func partHash(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:8])
	}
	b, _ := json.Marshal(withoutCacheMarkers(v))
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

func withoutCacheMarkers(v any) any {
	switch t := v.(type) {
	case map[string]any:
		delete(t, "cache_control")
		for k, x := range t {
			t[k] = withoutCacheMarkers(x)
		}
	case []any:
		for i, x := range t {
			t[i] = withoutCacheMarkers(x)
		}
	}
	return v
}

// turnCounts remembers each recording's count by its request log row, so a
// chat paged in windows reads each recording into Messages once rather than
// on every window: a recording never changes, held or stored. Bounded by starting over, which only costs the
// reading again.
type turnCounts struct {
	mu sync.Mutex
	m  map[int64]turnCount
}

const maxTurnCounts = 20000

func (c *turnCounts) get(id int64) (turnCount, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.m[id]
	return t, ok
}

func (c *turnCounts) put(id int64, t turnCount) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) >= maxTurnCounts {
		c.m = map[int64]turnCount{}
	}
	c.m[id] = t
}
