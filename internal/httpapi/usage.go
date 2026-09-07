package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/nebuloss/claudication/internal/store"
	"github.com/nebuloss/claudication/internal/version"
)

// recordUsage files one relayed request.
//
// Deliberately best-effort and never on the caller's critical path: this is
// bookkeeping, and a write failure should cost a row in a chart, not the
// request. It runs with its own context because the request's is cancelled the
// moment the client hangs up — which is exactly when a failed stream is most
// worth recording.
func (s *Server) recordUsage(e store.UsageEvent) {
	if !s.cfg.Usage.Enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.store.RecordUsage(ctx, e); err != nil {
		s.log.Warn("could not record usage", "err", err)
	}
}

// runUsagePruner keeps the event table bounded. Retention is the only thing
// that does — an event table grows for as long as the gateway is used.
func (s *Server) runUsagePruner() {
	if !s.cfg.Usage.Enabled() {
		return
	}
	prune := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		n, err := s.store.PruneUsage(ctx, s.cfg.Usage.Retention())
		if err != nil {
			s.log.Warn("could not prune usage", "err", err)
			return
		}
		if n > 0 {
			s.log.Info("pruned usage events", "rows", n,
				"older_than_days", s.cfg.Usage.RetentionDays)
		}
	}

	// Once at startup, because a gateway that was down for a month should not
	// wait another day to catch up, then daily.
	prune()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopSweeper:
			return
		case <-ticker.C:
			prune()
		}
	}
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Usage.Enabled() {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}

	window := s.cfg.Usage.Window()
	if v := r.URL.Query().Get("days"); v != "" {
		if days, err := strconv.Atoi(v); err == nil && days > 0 {
			if days > s.cfg.Usage.RetentionDays {
				days = s.cfg.Usage.RetentionDays
			}
			window = time.Duration(days) * 24 * time.Hour
		}
	}

	report, err := s.store.Usage(r.Context(), time.Now().Add(-window))
	if err != nil {
		s.log.Error("usage report", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read usage")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":        true,
		"days":           int(window.Hours() / 24),
		"retention_days": s.cfg.Usage.RetentionDays,
		"report":         report,
	})
}

type requestJSON struct {
	At           string `json:"at"`
	KeyName      string `json:"key_name,omitempty"`
	AccountEmail string `json:"account_email,omitempty"`
	Model        string `json:"model,omitempty"`
	Path         string `json:"path"`
	Status       int    `json:"status"`
	Streaming    bool   `json:"streaming"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	CacheTokens  int    `json:"cache_tokens"`
	DurationMS   int64  `json:"duration_ms"`
	Error        string `json:"error,omitempty"`
}

func (s *Server) handleRecentRequests(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Usage.Enabled() {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "requests": []requestJSON{}})
		return
	}

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	events, err := s.store.RecentUsage(r.Context(), limit)
	if err != nil {
		s.log.Error("recent requests", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read recent requests")
		return
	}

	out := make([]requestJSON, 0, len(events))
	for _, e := range events {
		out = append(out, requestJSON{
			At:           e.At.UTC().Format(time.RFC3339),
			KeyName:      e.KeyName,
			AccountEmail: e.AccountEmail,
			Model:        e.Model,
			Path:         e.Path,
			Status:       e.Status,
			Streaming:    e.Streaming,
			InputTokens:  e.InputTokens,
			OutputTokens: e.OutputTokens,
			CacheTokens:  e.CacheReadTokens + e.CacheWriteTokens,
			DurationMS:   e.Duration.Milliseconds(),
			Error:        e.Error,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "requests": out})
}

// handleOverview answers "is this working, and what do I point at it" in one
// request, so the first screen does not need four.
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"version":    version.Version,
		"commit":     version.Commit,
		"listen":     s.cfg.Listen,
		"started_at": s.startedAt.UTC().Format(time.RFC3339),
		"uptime_s":   int64(time.Since(s.startedAt).Seconds()),

		"usage_enabled": s.cfg.Usage.Enabled(),
	}

	accounts, err := s.store.ListAccounts(r.Context())
	if err != nil {
		s.log.Error("overview accounts", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read accounts")
		return
	}
	var usable, expiring int
	for _, a := range accounts {
		if !a.Disabled() && !a.Expired() {
			usable++
		}
		if a.NeedsReauthSoon() {
			expiring++
		}
	}
	out["accounts"] = map[string]any{
		"total": len(accounts), "usable": usable, "needs_reauth_soon": expiring,
	}

	keys, err := s.store.ListKeys(r.Context())
	if err != nil {
		s.log.Error("overview keys", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read keys")
		return
	}
	active := 0
	for _, k := range keys {
		if !k.Revoked() {
			active++
		}
	}
	out["keys"] = map[string]any{"total": len(keys), "active": active}

	// The headline numbers only; the Usage tab is where the breakdown lives.
	if s.cfg.Usage.Enabled() {
		if rep, err := s.store.Usage(r.Context(), time.Now().Add(-24*time.Hour)); err == nil {
			out["last_24h"] = rep.Totals
		}
	}

	// Whether the gateway can serve a request right now is not the same
	// question as whether it has accounts: they may all be cooling down.
	out["ready"] = usable > 0
	writeJSON(w, http.StatusOK, out)
}
