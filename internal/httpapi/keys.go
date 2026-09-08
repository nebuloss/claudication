package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"claudication/internal/store"
)

// Client API keys, from the admin API.
//
// These were CLI-only until now, which made the UI useless for the thing the
// gateway is for: you cannot point Claude Code at a proxy without a key to
// give it.

type keyJSON struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Display    string `json:"display"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	RPMLimit   int    `json:"rpm_limit"`
	// TokenBudget is the ceiling per rolling day, 0 for unlimited; SpentToday
	// is what has been counted against it. Both are reported so the UI can show
	// how close a key is rather than only whether it has already been refused.
	TokenBudget int64 `json:"token_budget"`
	SpentToday  int64 `json:"spent_today"`
	// Traffic over the reporting window, so a key nobody uses is visible.
	Requests int64 `json:"requests"`
	Tokens   int64 `json:"tokens"`
}

func toKeyJSON(k store.APIKey, use store.UsageBucket) keyJSON {
	out := keyJSON{
		ID:        k.ID,
		Name:      k.Name,
		Display:   k.Display(),
		CreatedAt: k.CreatedAt.UTC().Format(time.RFC3339),
		RPMLimit:    k.RPMLimit,
		TokenBudget: k.TokenBudget,
		Requests:    use.Requests,
		Tokens:    use.InputTokens + use.OutputTokens + use.CacheTokens,
	}
	if k.LastUsedAt != nil {
		out.LastUsedAt = k.LastUsedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.store.ListKeys(r.Context())
	if err != nil {
		s.log.Error("list api keys", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not list keys")
		return
	}

	// Usage is a nicety; a failure here should not hide the keys themselves.
	usage, err := s.store.KeyUsage(r.Context(), time.Now().Add(-s.cfg.Usage.Window()))
	if err != nil {
		s.log.Warn("key usage unavailable", "err", err)
		usage = map[string]store.UsageBucket{}
	}

	// Spend against the budget is a different window from the report above, so
	// it takes its own pass — one grouped query for every key, rather than a
	// lookup each. Also a nicety: a key still lists without it.
	var spend map[string]store.UsageBucket
	if s.cfg.Usage.Enabled() {
		if spend, err = s.store.KeyUsage(r.Context(), time.Now().Add(-BudgetWindow)); err != nil {
			s.log.Warn("key budget usage unavailable", "err", err)
			spend = map[string]store.UsageBucket{}
		}
	}

	out := make([]keyJSON, 0, len(keys))
	for _, k := range keys {
		j := toKeyJSON(k, usage[k.ID])
		if b, ok := spend[k.ID]; ok {
			j.SpentToday = b.InputTokens + b.OutputTokens + b.CacheTokens
		}
		out = append(out, j)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"keys":         out,
		"window_days":  int(s.cfg.Usage.Window().Hours() / 24),
		"budget_hours": int(BudgetWindow.Hours()),
	})
}

// handleCreateKey mints a key and returns the plaintext — once, here, and
// never again. The store keeps only sha256(key) and a lookup prefix.
func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		RPMLimit    int    `json:"rpm_limit"`
		TokenBudget int64  `json:"token_budget"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "a name is required")
		return
	}
	if body.RPMLimit < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request",
			"rpm_limit cannot be negative (0 means the global default)")
		return
	}
	if body.TokenBudget < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request",
			"token_budget cannot be negative (0 means unlimited)")
		return
	}

	key, plaintext, err := s.store.CreateKey(r.Context(), body.Name, body.RPMLimit, body.TokenBudget)
	if err != nil {
		s.log.Error("create api key", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not create the key")
		return
	}

	s.log.Info("api key created", "id", key.ID, "name", key.Name,
		"ip", clientIPFrom(r.Context()))
	writeJSON(w, http.StatusOK, map[string]any{
		"key":       toKeyJSON(key, store.UsageBucket{}),
		"plaintext": plaintext,
	})
}

// handleUpdateKey changes a key's name, rate limit or token budget. The secret
// is not touched, so nothing that already holds the key has to be told anything.
func (s *Server) handleUpdateKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		RPMLimit    int    `json:"rpm_limit"`
		TokenBudget int64  `json:"token_budget"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "a name is required")
		return
	}
	if body.RPMLimit < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request",
			"rpm_limit cannot be negative (0 means the global default)")
		return
	}
	if body.TokenBudget < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request",
			"token_budget cannot be negative (0 means unlimited)")
		return
	}

	id := r.PathValue("id")
	switch err := s.store.UpdateKey(r.Context(), id, body.Name, body.RPMLimit, body.TokenBudget); {
	case errors.Is(err, store.ErrKeyNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such key")
		return
	case err != nil:
		s.log.Error("update api key", "err", err, "id", id)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not update the key")
		return
	}
	// Drop the cached spend so a raised or lowered budget takes effect on the
	// next request rather than up to a refresh interval later — an operator
	// raising a budget to unblock a client should not have to wait.
	s.budgets.forget(id)
	s.log.Info("api key updated", "id", id, "name", body.Name,
		"rpm_limit", body.RPMLimit, "token_budget", body.TokenBudget,
		"ip", clientIPFrom(r.Context()))

	keys, err := s.store.ListKeys(r.Context())
	if err == nil {
		for _, k := range keys {
			if k.ID == id {
				writeJSON(w, http.StatusOK, map[string]any{"key": toKeyJSON(k, store.UsageBucket{})})
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// handleDeleteKey withdraws a key. There is no revoke beside it: revocation
// could not be undone either, so it was this under another name.
func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	switch err := s.store.DeleteKey(r.Context(), id); {
	case errors.Is(err, store.ErrKeyNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such key")
		return
	case err != nil:
		s.log.Error("delete api key", "err", err, "id", id)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not delete the key")
		return
	}
	s.budgets.forget(id)
	s.log.Info("api key deleted", "id", id, "ip", clientIPFrom(r.Context()))
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
