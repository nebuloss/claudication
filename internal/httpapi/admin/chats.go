package admin

import (
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
	out := make([]RequestJSON, 0, len(events))
	for _, e := range events {
		out = append(out, s.toRequestJSON(e))
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
