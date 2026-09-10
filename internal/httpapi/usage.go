package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"claudication/internal/store"
	"claudication/internal/upstream"
	"claudication/internal/version"
)

// recordUsage files one relayed request.
//
// Deliberately best-effort and never on the caller's critical path: this is
// bookkeeping, and a write failure should cost a row in a chart, not the
// request. It runs with its own context because the request's is cancelled the
// moment the client hangs up — which is exactly when a failed stream is most
// worth recording.
// budget is the key's ceiling, so an unlimited key can skip the tracker
// entirely rather than taking its lock to discover it has nothing to update.
func (s *Server) recordUsage(e store.UsageEvent, budget int64) {
	if !s.cfg.Usage.Enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.store.RecordUsage(ctx, e); err != nil {
		s.log.Warn("could not record usage", "err", err)
		return
	}
	// Credit the budget tracker with what this cost, so the cached figure keeps
	// up between refreshes rather than letting a burst through on a reading
	// taken half a minute ago. Only for a key that has a ceiling: for every
	// other key there is nothing to keep up with.
	if budget > 0 && e.KeyID != "" {
		s.budgets.add(e.KeyID, e.Tokens())
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
	// ErrorKind names a refusal whose message does not mean what it says.
	// Computed on read rather than stored: it is a reading of the recorded
	// text, and one that will get better as more of these are identified.
	ErrorKind string `json:"error_kind,omitempty"`
}

func (s *Server) handleRecentRequests(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Usage.Enabled() {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "requests": []requestJSON{}})
		return
	}

	// Bounded, because the caller chooses it. The activity list asks for 50;
	// ?limit=10000000 would otherwise load ten million rows into memory and
	// serialise them, which a mistyped URL is enough to do. A session is
	// needed to get here, but an operator should not be able to take their own
	// gateway down with a typo.
	const defaultLimit, maxLimit = 50, 1000
	limit := defaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = min(n, maxLimit)
		}
	}

	// An unreadable cursor starts from the top rather than failing: a bookmark
	// from an older release, or a truncated URL, should show the newest
	// requests, not an error.
	after, _ := store.ParseUsageCursor(r.URL.Query().Get("after"))

	events, next, err := s.store.RecentUsage(r.Context(), limit, after)
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
			ErrorKind:    string(upstream.ClassifyRefusal(e.Error)),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":  true,
		"requests": out,
		// Empty when there is nothing after this page, so the UI can stop
		// offering more rather than discovering it by fetching none.
		"next_cursor": next.String(),
	})
}

// handleOverview answers "is this working, and what do I point at it" in one
// request, so the first screen does not need four.
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"version": version.Version,
		"commit":  version.Commit,
		"listen":  s.cfg.Listen,
		// What a client should point at, and whether the browser's own origin
		// is a safe guess for it. Split listeners mean the UI is on the admin
		// address and the relay is elsewhere, so the origin would be wrong —
		// see PublicURL.
		"public_url":  s.cfg.PublicURL,
		"admin_split": s.cfg.AdminListen != "",
		"started_at":  s.startedAt.UTC().Format(time.RFC3339),
		"uptime_s":    int64(time.Since(s.startedAt).Seconds()),

		"usage_enabled": s.cfg.Usage.Enabled(),
	}

	accounts, err := s.store.ListAccounts(r.Context())
	if err != nil {
		s.log.Error("overview accounts", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read accounts")
		return
	}
	var expiring int
	for _, a := range accounts {
		if a.NeedsReauthSoon() {
			expiring++
		}
	}

	// Readiness is the pool's answer, not a second guess from the account
	// rows. Cooldowns are held in memory, so counting rows could not see them:
	// every account could be sitting out a rate limit and this screen would
	// still say the gateway was ready to serve.
	status, err := s.pool.Status(r.Context(), "anthropic")
	if err != nil {
		s.log.Error("overview pool status", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read accounts")
		return
	}
	out["accounts"] = map[string]any{
		"total":             len(accounts),
		"usable":            status.Usable,
		"cooling":           len(status.Cooling),
		"needs_reauth_soon": expiring,
		// Distinct from "soon": these are already past saving on their own.
		"needs_reauth": status.NeedsReauth,
	}

	keys, err := s.store.ListKeys(r.Context())
	if err != nil {
		s.log.Error("overview keys", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read keys")
		return
	}
	// Every key that exists is usable: withdrawing one deletes it, so there is
	// no second count to keep.
	out["keys"] = map[string]any{"total": len(keys)}

	// The headline numbers only; the Usage tab is where the breakdown lives.
	// Totals rather than Usage: the five breakdowns this screen never renders
	// were the bulk of the query behind the first page after sign-in.
	if s.cfg.Usage.Enabled() {
		if totals, err := s.store.Totals(r.Context(), time.Now().Add(-24*time.Hour)); err == nil {
			out["last_24h"] = totals
		}
	}

	// Whether the gateway can serve a request right now is not the same
	// question as whether it has accounts: they may all be cooling down.
	out["ready"] = status.Serving != ""
	writeJSON(w, http.StatusOK, out)
}
