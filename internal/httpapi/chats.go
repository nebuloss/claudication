package httpapi

import (
	"net/http"
	"strconv"
	"time"
)

// The chat screens.
//
// Two endpoints rather than one: the list is a rollup that has to stay cheap
// because it is what the screen opens on, and a single conversation's events
// are a drill-down nobody pays for until they ask. Folding the second into the
// first would put every request of every chat on the wire to render a table of
// totals.

// chatWindow is the time window a chat request asks for, bounded by retention.
//
// Shared with handleUsage's rule rather than reimplemented: a chat list over a
// different window than the charts beside it is a screen that appears to
// disagree with itself.
func (s *Server) chatWindow(r *http.Request) time.Duration {
	window := s.cfg.Usage.Window()
	if v := r.URL.Query().Get("days"); v != "" {
		if days, err := strconv.Atoi(v); err == nil && days > 0 {
			if days > s.cfg.Usage.RetentionDays {
				days = s.cfg.Usage.RetentionDays
			}
			window = time.Duration(days) * 24 * time.Hour
		}
	}
	return window
}

// handleChats answers the chat list.
func (s *Server) handleChats(w http.ResponseWriter, r *http.Request) {
	// Usage switched off means no events are being recorded at all, so there
	// is nothing to group. Saying so is better than an empty list, which reads
	// as "nobody has used this".
	if !s.cfg.Usage.Enabled() {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}

	window := s.chatWindow(r)
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	report, err := s.store.Chats(r.Context(), time.Now().Add(-window), limit)
	if err != nil {
		s.log.Error("chat list", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read chats")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":        true,
		"days":           int(window.Hours() / 24),
		"retention_days": s.cfg.Usage.RetentionDays,
		"report":         report,
	})
}

// handleChat answers one conversation's requests.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Usage.Enabled() {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "no conversation id")
		return
	}

	events, err := s.store.ChatEvents(r.Context(), id, 0)
	if err != nil {
		s.log.Error("chat events", "err", err, "chat", id)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read the chat")
		return
	}
	// An id nobody recognises is a 404 rather than an empty list: a chat that
	// has been pruned and one that never existed are the same thing from here,
	// and both mean the link the operator followed is stale.
	if len(events) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "no such chat, or its events have been pruned")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true,
		"id":      id,
		"events":  events,
	})
}
