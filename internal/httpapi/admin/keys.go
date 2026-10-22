package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/service/clientconfig"
	"claudication/internal/service/install"
	"claudication/internal/service/limits"
	"claudication/internal/store"
)

// KeyJSON is one API key as the admin API reports it. The secret itself is
// never in it; only its display form.
type KeyJSON struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Display    string `json:"display"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	RPMLimit   int    `json:"rpm_limit"`
	// RatePeriodS is what rpm_limit is per, in seconds. 60 unless an operator
	// chose otherwise; the name keeps its history rather than pretending the
	// column was always general.
	RatePeriodS int `json:"rate_period_s"`
	// TokenBudget is the ceiling per rolling day, 0 for unlimited; SpentToday
	// is what has been counted against it. Both are reported so the UI can show
	// how close a key is rather than only whether it has already been refused.
	TokenBudget int64 `json:"token_budget"`
	SpentToday  int64 `json:"spent_today"`
	// Traffic over the reporting window, so a key nobody uses is visible.
	Requests int64 `json:"requests"`
	Tokens   int64 `json:"tokens"`
	// Subscription is the key's estimated part of the Claude subscription's
	// 5-hour and 7-day windows; absent when usage history is off or the
	// accounts have not reported. See keyshare.go.
	Subscription *SubscriptionShare `json:"subscription,omitempty"`
	// ModelsOff are the models this key may not ask for; empty allows all.
	ModelsOff []string `json:"models_off"`
	// SecretKept says the key can be shown again, configs and all.
	SecretKept bool `json:"secret_kept"`
}

func (s *Admin) toKeyJSON(k store.APIKey, use store.UsageBucket) KeyJSON {
	out := KeyJSON{
		ID:          k.ID,
		Name:        k.Name,
		Display:     k.Display(),
		CreatedAt:   k.CreatedAt.UTC().Format(time.RFC3339),
		RPMLimit:    k.RPMLimit,
		RatePeriodS: int(k.Period().Seconds()),
		TokenBudget: k.TokenBudget,
		Requests:    use.Requests,
		Tokens:      use.InputTokens + use.OutputTokens + use.CacheTokens,
		ModelsOff:   k.ModelsOff,
		SecretKept:  k.SecretKept,
	}
	if out.ModelsOff == nil {
		out.ModelsOff = []string{}
	}
	if k.LastUsedAt != nil {
		out.LastUsedAt = k.LastUsedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// keyListEnvelope is a list of keys or clients with what the UI needs to read
// their figures: the window, and the two defaults a bare 0 stands for.
func (s *Admin) keyListEnvelope(field string, list any) map[string]any {
	return map[string]any{
		field:         list,
		"window_days": int(s.cfg.Usage.Window().Hours() / 24),
		// So the UI can name the two defaults rather than showing a bare 0,
		// which means different things in the two columns: no ceiling for a
		// budget, the server's own limit for a rate.
		"budget_hours": int(limits.BudgetWindow.Hours()),
		"default_rpm":  s.cfg.Limits.RequestsPerMinute,
	}
}

// keyStats is what every key's row is shown with beyond the key itself, read
// once for the whole list: its traffic over the reporting window, its spend
// against its budget, and its share of the subscription. Each is a nicety —
// a failure leaves the figure out and the keys still list.
type keyStats struct {
	usage, spend map[string]store.UsageBucket
	windows      []accountWindows
}

// keyStats reads every key's figures, for a list; with only, one key's, for
// its own page. The subscription windows are read whole either way: a key's
// share of an account is of what every key sent it.
func (s *Admin) keyStats(ctx context.Context, only ...string) keyStats {
	var st keyStats
	var err error
	usage := s.store.KeyUsage
	if len(only) > 0 {
		usage = func(ctx context.Context, since time.Time) (map[string]store.UsageBucket, error) {
			return s.store.KeyUsageOf(ctx, only[0], since)
		}
	}
	if st.usage, err = usage(ctx, time.Now().Add(-s.cfg.Usage.Window())); err != nil {
		s.log.Warn("key usage unavailable", "err", err)
		st.usage = map[string]store.UsageBucket{}
	}
	// Spend against the budget is a different window from the report above,
	// so it takes its own pass — one grouped query for every key, rather than
	// a lookup each.
	if s.cfg.Usage.Enabled() {
		if st.spend, err = usage(ctx, time.Now().Add(-limits.BudgetWindow)); err != nil {
			s.log.Warn("key budget usage unavailable", "err", err)
			st.spend = nil
		}
		if st.windows, err = s.subscriptionWindows(ctx); err != nil {
			s.log.Warn("subscription shares unavailable", "err", err)
			st.windows = nil
		}
	}
	return st
}

// of is one key's row.
func (st keyStats) of(s *Admin, k store.APIKey) KeyJSON {
	j := s.toKeyJSON(k, st.usage[k.ID])
	if b, ok := st.spend[k.ID]; ok {
		j.SpentToday = b.InputTokens + b.OutputTokens + b.CacheTokens
	}
	if st.windows != nil {
		share := shareOf(k.ID, st.windows)
		j.Subscription = &share
	}
	return j
}

// keyLimitsBody is what create and update both accept.
type keyLimitsBody struct {
	Name        string `json:"name"`
	RPMLimit    int    `json:"rpm_limit"`
	RatePeriodS int    `json:"rate_period_s"`
	TokenBudget int64  `json:"token_budget"`
}

// decodeKeyLimits reads and validates the shared body, writing the refusal
// itself. Returns false when it has answered.
func decodeKeyLimits(w http.ResponseWriter, r *http.Request) (string, store.KeyLimits, bool) {
	var body keyLimitsBody
	if !httpx.DecodeJSON(w, r, &body) {
		return "", store.KeyLimits{}, false
	}
	name := strings.TrimSpace(body.Name)
	switch {
	case name == "":
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, "a name is required")
		return "", store.KeyLimits{}, false
	case body.RPMLimit < 0:
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest,
			"rpm_limit cannot be negative (0 means the global default)")
		return "", store.KeyLimits{}, false
	case body.TokenBudget < 0:
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest,
			"token_budget cannot be negative (0 means unlimited)")
		return "", store.KeyLimits{}, false
	case body.RatePeriodS < 0 || body.RatePeriodS > 7*24*3600:
		// A week is already far past anything a rate limit usefully means, and
		// an unbounded period is a bucket that refills so slowly it is really a
		// lifetime quota wearing a rate limit's name.
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest,
			"rate_period_s must be between 0 and one week")
		return "", store.KeyLimits{}, false
	}
	return name, store.KeyLimits{
		RPMLimit:    body.RPMLimit,
		RatePeriod:  time.Duration(body.RatePeriodS) * time.Second,
		TokenBudget: body.TokenBudget,
	}, true
}

// refuseConfigured answers 409 for a client already set up with its key, and
// reports whether it did. Its key and its install command are offered until
// the key is first used, and not after: a key that is in use somewhere has no
// reason to be shown again, and regenerating it is how a client is set up
// afresh.
func (s *Admin) refuseConfigured(w http.ResponseWriter, r *http.Request, id string) bool {
	c, err := s.store.Client(r.Context(), id)
	if err != nil || c.ConfiguredAt == nil {
		return false
	}
	httpx.WriteError(w, http.StatusConflict, "configured",
		"this client was set up on "+c.ConfiguredAt.UTC().Format("2006-01-02")+
			"; its key is no longer shown. Regenerate the key to set it up again")
	return true
}

// handleKeySecret shows a key's secret again, for its client configs.
//
// Behind the admin session like everything that can create, rotate or delete
// a key; whoever can do those can already mint a working credential. Logged
// all the same, so a reveal is on record. A key made before secrets were kept
// answers 409 secret_not_kept, and the UI offers a new secret instead.
func (s *Admin) handleKeySecret(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.refuseConfigured(w, r, id) {
		return
	}
	plaintext, err := s.store.KeySecret(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrKeyNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such key")
		return
	case errors.Is(err, store.ErrSecretNotKept):
		httpx.WriteError(w, http.StatusConflict, "secret_not_kept", err.Error())
		return
	case err != nil:
		s.log.Error("read api key secret", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not read the key")
		return
	}
	s.log.Warn("api key shown", "id", id, "ip", httpx.ClientIP(r.Context()))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"plaintext": plaintext})
}

// handleRotateKey issues a key a new secret and answers it once, in the shape
// creating a key does, so the admin UI shows it — and the client configs
// written with it — the same way. The old secret stops working at once.
func (s *Admin) handleRotateKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	key, plaintext, err := s.store.RotateKey(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrKeyNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such key")
		return
	case err != nil:
		s.log.Error("rotate api key", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not issue a new secret")
		return
	}
	// The old secret is in any link not yet fetched; a new one is made
	// with the new secret.
	s.install.Forget(id)
	s.log.Warn("api key rotated", "id", key.ID, "name", key.Name, "ip", httpx.ClientIP(r.Context()))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"key":       s.toKeyJSON(key, store.UsageBucket{}),
		"plaintext": plaintext,
	})
}

// handleUpdateKey changes a key's name, rate limit or token budget. The secret
// is not touched, so nothing that already holds the key has to be told anything.
func (s *Admin) handleUpdateKey(w http.ResponseWriter, r *http.Request) {
	name, limits, ok := decodeKeyLimits(w, r)
	if !ok {
		return
	}

	id := r.PathValue("id")
	switch err := s.store.UpdateKey(r.Context(), id, name, limits); {
	case errors.Is(err, store.ErrKeyNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such key")
		return
	case errors.Is(err, store.ErrInvalidName):
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, err.Error())
		return
	case err != nil:
		s.log.Error("update api key", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not update the key")
		return
	}
	// Drop the cached spend so a raised or lowered budget takes effect on the
	// next request rather than up to a refresh interval later — an operator
	// raising a budget to unblock a client should not have to wait.
	s.budgets.Forget(id)
	s.log.Info("api key updated", "id", id, "name", name,
		"rpm_limit", limits.RPMLimit, "rate_period", limits.Period().String(),
		"token_budget", limits.TokenBudget, "ip", httpx.ClientIP(r.Context()))

	// The change is committed; a failure to read it back is not a failure to
	// make it, so it is not answered as one.
	if k, err := s.store.Key(r.Context(), id); err == nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"key": s.toKeyJSON(k, store.UsageBucket{})})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// handleDeleteKey withdraws a key. There is no revoke beside it: revocation
// could not be undone either, so it was this under another name.
func (s *Admin) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	switch err := s.store.DeleteKey(r.Context(), id); {
	case errors.Is(err, store.ErrKeyNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such key")
		return
	case err != nil:
		s.log.Error("delete api key", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not delete the key")
		return
	}
	s.budgets.Forget(id)
	s.install.Forget(id)
	s.log.Info("api key deleted", "id", id, "ip", httpx.ClientIP(r.Context()))
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// linkBody is what the install and update commands are asked with: the
// address the client is to be pointed at, which the page knows and the
// gateway may not (internal/service/clientconfig, Base).
type linkBody struct {
	Base string `json:"base"`
}

// typedClient reads the client a key path names, for its install or update
// command, and the address its files are written for; it answers itself when
// it cannot. A custom client has no files, and no command.
func (s *Admin) typedClient(w http.ResponseWriter, r *http.Request) (store.Client, string, bool) {
	var body linkBody
	if !httpx.DecodeJSON(w, r, &body) {
		return store.Client{}, "", false
	}
	base, err := clientconfig.Base(body.Base)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, err.Error())
		return store.Client{}, "", false
	}
	c, ok := s.loadClient(w, r)
	if !ok {
		return store.Client{}, "", false
	}
	if !clientconfig.Writes(c.Type) {
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest,
			"a custom client has no files to install; it is pointed at the gateway by hand")
		return store.Client{}, "", false
	}
	return c, base, true
}

// handleKeyInstall writes a client's files for the install address, key and
// all, keeps them under a token, and answers the token that fetches them and
// when it stops working.
//
// The files are the gateway's own (internal/service/clientconfig): the
// client's names, each with what it serves now, and the key read from the
// store — which is why a key whose secret was not kept cannot be installed
// this way, and the page offers a new secret instead.
func (s *Admin) handleKeyInstall(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.refuseConfigured(w, r, id) {
		return
	}
	c, base, ok := s.typedClient(w, r)
	if !ok {
		return
	}
	plaintext, err := s.store.KeySecret(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrSecretNotKept):
		httpx.WriteError(w, http.StatusConflict, "secret_not_kept", err.Error())
		return
	case err != nil:
		httpx.Internal(w, s.log, "read the key's secret", err)
		return
	}
	setup := s.setupOf(r, c)
	files := clientconfig.Install(c.Type, c.Spec().Label, setup, base, plaintext)
	token, expires, err := s.install.Create(install.Bundle{ClientID: c.ID, Key: c.Name, Files: files})
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"expires_at": expires.UTC().Format(time.RFC3339),
	})
}

// handleKeyUpdate writes a configured client's update files, which carry no
// key, keeps them under a token, and answers the token that fetches them, as
// handleKeyInstall does.
//
// Once a client is set up its key is never shown again, and its model names
// and what they serve go on changing; an update is how its machines catch up
// without the key going anywhere. The files are written with the key left
// out and its paths left unowned (internal/service/clientconfig, Update), and
// the MCP server's update_config makes the same command for the client's own
// agent (internal/httpapi/agent): one writer, so the two cannot differ.
func (s *Admin) handleKeyUpdate(w http.ResponseWriter, r *http.Request) {
	c, base, ok := s.typedClient(w, r)
	if !ok {
		return
	}
	if c.ConfiguredAt == nil {
		httpx.WriteError(w, http.StatusConflict, "not_configured",
			"this client is not set up yet; its install command sets it up, key and all")
		return
	}
	setup := s.setupOf(r, c)
	token, expires, err := clientconfig.MakeUpdate(r.Context(), s.store, s.install, c, setup, base)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	s.log.Info("client update command made", "id", c.ID, "ip", httpx.ClientIP(r.Context()))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"expires_at": expires.UTC().Format(time.RFC3339),
	})
}
