package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/service/titles"
	"claudication/internal/store"
)

// chatWindow is the time window a chat request asks for, bounded by retention.
//
// Shared with handleUsage's rule rather than reimplemented: a chat list over a
// different window than the charts beside it is a screen that appears to
// disagree with itself.
func (s *Admin) chatWindow(r *http.Request) time.Duration {
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
func (s *Admin) handleChats(w http.ResponseWriter, r *http.Request) {
	// Usage switched off means no events are being recorded at all, so there
	// is nothing to group. Saying so is better than an empty list, which reads
	// as "nobody has used this".
	if !s.cfg.Usage.Enabled() {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"enabled": false})
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
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not read chats")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
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
func (s *Admin) handleSetChatTitles(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
		Capture *bool `json:"capture"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request",
			`expected {"enabled": true|false} or {"capture": true|false}`)
		return
	}
	if body.Enabled == nil && body.Capture == nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request",
			"one of enabled or capture is required")
		return
	}

	for _, change := range []struct {
		key   string
		value *bool
		what  string
	}{
		{titles.SettingGenerate, body.Enabled, "generate"},
		{titles.SettingCapture, body.Capture, "capture"},
	} {
		if change.value == nil {
			continue
		}
		if err := s.titles.Set(r.Context(), change.key, *change.value); err != nil {
			s.log.Error("could not store the chat-title switch", "which", change.what, "err", err)
			httpx.WriteError(w, http.StatusInternalServerError, "api_error", "could not store the setting")
			return
		}
		s.log.Warn("chat titles switched", "which", change.what, "enabled", *change.value)
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"chat_titles":         s.titles.On(),
		"chat_titles_capture": s.titles.Capturing(),
	})
}

// handleRetitleChat renames a chat now and answers with the new name.
//
// 202 only when the gateway has restarted since the chat's last request and
// so has nothing to name it from: it is then renamed at that next request,
// and the screen says so. See titles.Titler.RefreshNow.
func (s *Admin) handleRetitleChat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "no conversation id")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	title, pending, err := s.titles.RefreshNow(ctx, id)
	switch {
	case errors.Is(err, titles.ErrGenerateOff):
		httpx.WriteError(w, http.StatusConflict, "titles_off", err.Error())
	case err != nil:
		s.log.Warn("could not refresh a chat title", "chat", id, "err", err)
		httpx.WriteError(w, http.StatusBadGateway, "title_failed", "could not name the chat: "+err.Error())
	case pending:
		httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"pending": true})
	default:
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"title": title, "title_manual": false})
	}
}

// handleSetChatTitle stores a title the operator typed.
//
// It holds against every automatic rename (migration 024), so the one name
// known to be right is not replaced by a guess; Refresh hands it back. Works
// with generation off, since nothing is spent.
func (s *Admin) handleSetChatTitle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil || id == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", `expected {"title": "…"}`)
		return
	}
	if strings.TrimSpace(body.Title) == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "a title cannot be empty")
		return
	}
	title, err := s.store.SetManualTitle(r.Context(), id, body.Title)
	if err != nil {
		s.log.Error("could not store a chat title", "err", err, "chat", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not store the title")
		return
	}
	// A refresh asked for earlier would otherwise rename over what was just
	// typed at the next request.
	s.titles.Forget(id)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"title": title, "title_manual": true})
}

// handleSetTitleMode switches a chat between automatic and manual naming.
//
// Manual keeps the title it has and stops the gateway renaming it; automatic
// lets the growth rules apply again, without renaming anything by itself — a
// rename right now is what Refresh is for.
func (s *Admin) handleSetTitleMode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Manual *bool `json:"manual"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil ||
		body.Manual == nil || id == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", `expected {"manual": true|false}`)
		return
	}
	found, err := s.store.SetTitleManual(r.Context(), id, *body.Manual)
	if err != nil {
		s.log.Error("could not set the title mode", "err", err, "chat", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not store the title mode")
		return
	}
	if !found {
		if *body.Manual {
			httpx.WriteError(w, http.StatusConflict, "no_title",
				"this chat has no title to keep yet; rename it to name it by hand")
			return
		}
		// Automatic is what an untitled chat already is.
	}
	if *body.Manual {
		s.titles.Forget(id)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"title_manual": *body.Manual})
}

// handleChat answers one conversation's requests.
func (s *Admin) handleChat(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Usage.Enabled() {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}

	id := r.PathValue("id")
	if id == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "no conversation id")
		return
	}

	events, err := s.store.ChatEvents(r.Context(), id, 0)
	if err != nil {
		s.log.Error("chat events", "err", err, "chat", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not read the chat")
		return
	}
	// An id nobody recognises is a 404 rather than an empty list: a chat that
	// has been pruned and one that never existed are the same thing from here,
	// and both mean the link the operator followed is stale.
	if len(events) == 0 {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "no such chat, or its events have been pruned")
		return
	}

	// The same shape the recent-requests list uses, through the same
	// conversion. store.UsageEvent carries no JSON tags, so returning it raw
	// would put Go field names on the wire and give this one endpoint a
	// vocabulary of its own.
	out := make([]RequestJSON, 0, len(events))
	for _, e := range events {
		out = append(out, toRequestJSON(e))
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
