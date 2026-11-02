package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"claudication/internal/httpapi/httpx"
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
	captured := s.capture.ForEvents()
	out := make([]RequestJSON, 0, len(events))
	for _, e := range events {
		row := s.toRequestJSON(e)
		row.Capture = captured[e.ID]
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

// TurnJSON is what one recorded request of a chat added to it: the messages
// new since the chat's previous turn on its model, and the answer.
type TurnJSON struct {
	// Event is the request's row, and Capture its recording.
	Event   int64  `json:"event"`
	Capture uint64 `json:"capture"`
	// Offset is how many messages came before the added ones, which numbers
	// them as the conversation does.
	Offset int `json:"offset"`
	// Restarted is a request that carried fewer messages than the turn
	// before it on its model: the client compacted or started over, or it is
	// a side request on the same model. Its messages are all shown.
	Restarted bool              `json:"restarted,omitempty"`
	Added     []json.RawMessage `json:"added"`
	Answer    json.RawMessage   `json:"answer,omitempty"`
	// Unread is why the request could not be read as a conversation.
	Unread string `json:"unread,omitempty"`
}

// handleChatTranscript answers what each recorded request of a chat added
// to it, oldest first: the chat read as a transcript, from the recordings
// still held (internal/service/capture).
//
// A client resends the whole conversation every turn, so a request's own
// body is mostly the turns before it. What is new is what follows the
// previous request on the same model, its answer included — counted, not
// compared: Claude Code moves its cache markers between turns, so the same
// message is not the same bytes twice, and the count is what holds. Each
// model is followed on its own, since a client's side requests — titles,
// subagents — run beside the main thread on other models.
func (s *Admin) handleChatTranscript(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	events, err := s.store.ChatEvents(r.Context(), id, 0)
	if err != nil {
		s.log.Error("chat events", "err", err, "chat", id)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not read the chat")
		return
	}
	captured := s.capture.ForEvents()
	seen := map[string]int{}
	turns := []TurnJSON{}
	for _, e := range events {
		cid, ok := captured[e.ID]
		if !ok {
			continue
		}
		x, ok := s.capture.Get(cid)
		if !ok {
			continue
		}
		t := TurnJSON{Event: e.ID, Capture: cid, Added: []json.RawMessage{}}
		l := s.legJSON(&x.Client)
		if l.Request.Messages == nil {
			t.Unread = l.Request.MessagesError
			turns = append(turns, t)
			continue
		}
		var req struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(l.Request.Messages, &req); err != nil {
			t.Unread = err.Error()
			turns = append(turns, t)
			continue
		}
		if before, ok := seen[e.Model]; ok {
			if len(req.Messages) >= before {
				t.Offset = before
			} else {
				t.Restarted = true
			}
		}
		t.Added = req.Messages[t.Offset:]
		if l.Response != nil && l.Response.Messages != nil {
			t.Answer = l.Response.Messages
		}
		next := len(req.Messages)
		if t.Answer != nil {
			next++
		}
		seen[e.Model] = next
		turns = append(turns, t)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"id": id, "turns": turns})
}
