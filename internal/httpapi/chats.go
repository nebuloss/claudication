package httpapi

import (
	"encoding/json"
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

// handleSetChatTitles turns chat naming on or off.
//
// Its own endpoint rather than a config value, for the reason the API switches
// have one: this decides whether the gateway may spend the operator's
// subscription on its own behalf, and the answer to "stop doing that" cannot be
// "edit a file and restart".
// Two switches, because they are two different bargains. Capture only notices
// a name already going past and costs nothing; generation spends the
// operator's subscription. Either may be sent alone.
func (s *Server) handleSetChatTitles(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
		Capture *bool `json:"capture"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request",
			`expected {"enabled": true|false} or {"capture": true|false}`)
		return
	}
	if body.Enabled == nil && body.Capture == nil {
		writeError(w, http.StatusBadRequest, "invalid_request",
			"one of enabled or capture is required")
		return
	}

	for _, change := range []struct {
		key   string
		value *bool
		what  string
	}{
		{titleSetting, body.Enabled, "generate"},
		{captureSetting, body.Capture, "capture"},
	} {
		if change.value == nil {
			continue
		}
		if err := s.titles.set(r.Context(), change.key, *change.value); err != nil {
			s.log.Error("could not store the chat-title switch", "which", change.what, "err", err)
			writeError(w, http.StatusInternalServerError, "api_error", "could not store the setting")
			return
		}
		s.log.Warn("chat titles switched", "which", change.what, "enabled", *change.value)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"chat_titles":         s.titles.on(),
		"chat_titles_capture": s.titles.capturing(),
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

	// The same shape the recent-requests list uses, through the same
	// conversion. store.UsageEvent carries no JSON tags, so returning it raw
	// would put Go field names on the wire and give this one endpoint a
	// vocabulary of its own.
	out := make([]requestJSON, 0, len(events))
	for _, e := range events {
		out = append(out, toRequestJSON(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":  true,
		"id":       id,
		"requests": out,
	})
}
