package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/provider"
	"claudication/internal/store"
)

// AccountJSON is one connected account as the admin API reports it.
type AccountJSON struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Email         string `json:"email"`
	ExpiresAt     string `json:"expires_at"`
	CreatedAt     string `json:"created_at"`
	LastRefreshAt string `json:"last_refresh_at,omitempty"`
	LastUsedAt    string `json:"last_used_at,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	Expired       bool   `json:"expired"`
	Disabled      bool   `json:"disabled"`
	// When re-authorisation becomes unavoidable. Omitted when the provider did
	// not say; refreshing cannot push it back.
	RefreshExpiresAt string `json:"refresh_expires_at,omitempty"`
	ReauthDaysLeft   *int   `json:"reauth_days_left,omitempty"`
	NeedsReauthSoon  bool   `json:"needs_reauth_soon,omitempty"`
	// NeedsReauth is the terminal one: the refresh token was refused for good,
	// or its window has closed. Only a browser fixes it.
	NeedsReauth   bool   `json:"needs_reauth,omitempty"`
	RefreshDeadAt string `json:"refresh_dead_at,omitempty"`

	// What the subscription has left. Absent until a response has been seen.
	Quota *quotaJSON `json:"quota,omitempty"`
	// Traffic this gateway put through the account, over the report window.
	Requests int64 `json:"requests"`
	Tokens   int64 `json:"tokens"`
	// Position in the priority list, and whether this is the one that would
	// serve the next request.
	Position int  `json:"position"`
	Serving  bool `json:"serving"`
	// When a sidelined account comes back, if it is sitting one out. This is
	// the pool's in-memory state, so it is empty after a restart.
	CoolingUntil string `json:"cooling_until,omitempty"`
	// ModelsOff are the models turned off for this account, as switched.
	ModelsOff []string `json:"models_off,omitempty"`
}

// quotaJSON is what the provider last reported: the two headline windows,
// plus the provider's own rows — for Anthropic the same list `/usage` prints,
// including the per-model weekly limits the headline windows do not cover;
// for ChatGPT one row per window, labelled by its length.
//
// Percentages are 0-100, as the endpoints send them.
type quotaJSON struct {
	UpdatedAt      string           `json:"updated_at"`
	FiveHourUtil   float64          `json:"five_hour_util"`
	FiveHourReset  string           `json:"five_hour_reset,omitempty"`
	FiveHourStatus string           `json:"five_hour_status,omitempty"`
	SevenDayUtil   float64          `json:"seven_day_util"`
	SevenDayReset  string           `json:"seven_day_reset,omitempty"`
	SevenDayStatus string           `json:"seven_day_status,omitempty"`
	Allowed        bool             `json:"allowed"`
	Limits         []provider.Limit `json:"limits,omitempty"`
}

// toAccountJSON is one account as the admin API reports it. prov reads the
// quota's detail back, which is in the provider's own shape; nil leaves the
// rows out.
func toAccountJSON(a store.Account, prov provider.Accounts) AccountJSON {
	out := AccountJSON{
		ID:        a.ID,
		Provider:  a.Provider,
		Email:     a.Email,
		ExpiresAt: a.ExpiresAt.UTC().Format(time.RFC3339),
		CreatedAt: a.CreatedAt.UTC().Format(time.RFC3339),
		LastError: a.LastError,
		Expired:   a.Expired(),
		Disabled:  a.Disabled(),
		ModelsOff: a.ModelsOff,
	}
	if a.LastRefreshAt != nil {
		out.LastRefreshAt = a.LastRefreshAt.UTC().Format(time.RFC3339)
	}
	if a.LastUsedAt != nil {
		out.LastUsedAt = a.LastUsedAt.UTC().Format(time.RFC3339)
	}
	if left, ok := a.RefreshWindow(); ok {
		out.RefreshExpiresAt = a.RefreshExpiresAt.UTC().Format(time.RFC3339)
		// Ceiling, like the client's banner: "expires in 1 day" while any part
		// of that day remains.
		days := int((left + 24*time.Hour - time.Second) / (24 * time.Hour))
		if days < 0 {
			days = 0
		}
		out.ReauthDaysLeft = &days
		out.NeedsReauthSoon = a.NeedsReauthSoon()
	}
	// Outside that block on purpose: a refresh token refused for good is the
	// one account state a human has to act on, and it does not depend on the
	// expiry window being known — an account can be refused long before its
	// window runs out, and one whose window we never learned would otherwise
	// never say so.
	out.NeedsReauth = a.NeedsReauth()
	if a.RefreshDeadAt != nil {
		out.RefreshDeadAt = a.RefreshDeadAt.UTC().Format(time.RFC3339)
	}
	if a.Quota.Known() {
		q := quotaJSON{
			FiveHourUtil:   a.Quota.FiveHourUtil,
			FiveHourStatus: a.Quota.FiveHourStatus,
			SevenDayUtil:   a.Quota.SevenDayUtil,
			SevenDayStatus: a.Quota.SevenDayStatus,
			Allowed:        a.Quota.Allowed(),
		}
		if !a.Quota.UpdatedAt.IsZero() {
			q.UpdatedAt = a.Quota.UpdatedAt.UTC().Format(time.RFC3339)
		}
		if !a.Quota.FiveHourReset.IsZero() {
			q.FiveHourReset = a.Quota.FiveHourReset.UTC().Format(time.RFC3339)
		}
		if !a.Quota.SevenDayReset.IsZero() {
			q.SevenDayReset = a.Quota.SevenDayReset.UTC().Format(time.RFC3339)
		}
		if a.Quota.Detail != "" && prov != nil {
			q.Limits = prov.Limits(a.Quota.Detail)
		}
		out.Quota = &q
	}
	return out
}

func (s *Admin) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.store.ListAccounts(r.Context())
	if err != nil {
		s.log.Error("list accounts", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not list accounts")
		return
	}

	// How much this gateway put through each account, which is a different
	// question from how much of the subscription is spent: other clients share
	// the same Claude account.
	usage, err := s.store.AccountUsage(r.Context(), time.Now().Add(-s.cfg.Usage.Window()))
	if err != nil {
		s.log.Warn("account usage unavailable", "err", err)
		usage = map[string]store.UsageBucket{}
	}

	// Ask the pool which account would serve rather than working it out again
	// here. This walk used to approximate pick() and could not see a cooldown
	// at all — cooldowns are in memory, not in the row — so an account sitting
	// out a rate limit still wore the "serving" badge.
	status, err := s.poolStatus(r.Context())
	if err != nil {
		s.log.Error("account pool status", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not read accounts")
		return
	}

	out := make([]AccountJSON, 0, len(accounts))
	for i, a := range accounts {
		j := s.accountJSON(a)
		j.Position = i
		if u, ok := usage[a.ID]; ok {
			j.Requests = u.Requests
			j.Tokens = u.InputTokens + u.OutputTokens + u.CacheTokens
		}
		j.Serving = status.Serving[a.ID]
		if until, ok := status.Cooling[a.ID]; ok {
			j.CoolingUntil = until.UTC().Format(time.RFC3339)
		}
		out = append(out, j)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"accounts":    out,
		"window_days": int(s.cfg.Usage.Window().Hours() / 24),
		// What the server will do next, so the UI can re-ask at the rate the
		// figures actually change instead of guessing at one. Asking faster
		// than this only ever returns the same numbers again.
		"usage_poll_s": int(s.cfg.Usage.PollWatched.D().Seconds()),
	})
}

// handleReorderAccounts takes the whole priority list rather than a swap: two
// operators reordering at once would otherwise interleave into an order
// neither asked for, and the UI already knows the list it wants.
func (s *Admin) handleReorderAccounts(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if len(body.IDs) == 0 {
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, "ids must not be empty")
		return
	}
	if err := s.store.SetAccountOrder(r.Context(), body.IDs); err != nil {
		s.log.Error("reorder accounts", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not save the order")
		return
	}
	s.log.Info("account priority changed", "ip", httpx.ClientIP(r.Context()))
	s.handleListAccounts(w, r)
}

// handleRefreshUsage re-polls one account on demand, so an operator watching
// the screen does not have to wait out the interval.
func (s *Admin) handleRefreshUsage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.poller.Refresh(r.Context(), id); err != nil {
		s.log.Warn("usage refresh failed", "account", id, "err", err)
		httpx.WriteError(w, http.StatusBadGateway, httpx.KindUpstream,
			"could not read the subscription usage: "+err.Error())
		return
	}
	account, err := s.store.Account(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such account")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"account": s.accountJSON(account)})
}

// accountJSON is toAccountJSON with the account's own provider.
func (s *Admin) accountJSON(a store.Account) AccountJSON {
	prov, _ := s.providers.Find(a.Provider)
	return toAccountJSON(a, prov)
}

func (s *Admin) handleTestAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	acct, err := s.store.Account(r.Context(), id)
	if errors.Is(err, store.ErrAccountNotFound) {
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such account")
		return
	}
	if err != nil {
		httpx.Internal(w, s.log, "read the account", err)
		return
	}

	prov, ok := s.providerFor(w, acct.Provider)
	if !ok {
		return
	}
	tokens, err := s.store.AccountTokens(r.Context(), id)
	if err != nil {
		httpx.Internal(w, s.log, "read the account's tokens", err)
		return
	}

	// Refresh first if the access token has expired, so "test" answers the
	// question the operator is actually asking: can this account serve traffic?
	//
	// Through the pool rather than directly, because the pool holds the
	// single-flight lock. Refresh tokens rotate, so a refresh here racing one
	// on the request path would leave the loser holding a spent token — which
	// is the failure this button exists to diagnose, not to cause.
	if acct.Expired() {
		if rerr := s.pool.Refresh(r.Context(), id); rerr == nil {
			if fresh, ferr := s.store.AccountTokens(r.Context(), id); ferr == nil {
				tokens = fresh
			}
		}
	}

	var body struct {
		Model string `json:"model"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)

	result := prov.Probe(r.Context(), s.httpClient,
		provider.Credential{Token: tokens.AccessToken, AccountID: acct.AccountUUID}, body.Model)
	if result.OK {
		s.store.MarkAccountUsed(r.Context(), id)
	} else {
		s.store.MarkAccountError(r.Context(), id, result.Error)
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

// handleRefreshAccount forces a token refresh now.
//
// Through the pool, which serialises refreshes per account: rotating refresh
// tokens mean two at once leave the loser holding a spent one, and a button
// the operator presses while traffic is flowing is exactly how that happens.
func (s *Admin) handleRefreshAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.Account(r.Context(), id); errors.Is(err, store.ErrAccountNotFound) {
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such account")
		return
	}

	if err := s.pool.Refresh(r.Context(), id); err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "refresh_failed", err.Error())
		return
	}

	acct, err := s.store.Account(r.Context(), id)
	if err != nil {
		httpx.Internal(w, s.log, "read the account", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"account": s.accountJSON(acct)})
}

// handleSetAccountDisabled pauses an account, or resumes it.
//
// Pausing keeps the credentials: the alternative was deleting the account,
// which revokes its refresh token upstream and means going through the browser
// consent flow again to undo. "Not this one this week" should not cost that.
func (s *Admin) handleSetAccountDisabled(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Disabled bool `json:"disabled"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	switch err := s.store.SetAccountDisabled(r.Context(), id, body.Disabled); {
	case errors.Is(err, store.ErrAccountNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such account")
		return
	case err != nil:
		s.log.Error("set account disabled", "err", err, "account", id)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not update the account")
		return
	}
	s.log.Info("account availability changed", "account", id, "disabled", body.Disabled,
		"ip", httpx.ClientIP(r.Context()))

	acct, err := s.store.Account(r.Context(), id)
	if err != nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]bool{"disabled": body.Disabled})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s.accountJSON(acct))
}

// handleDeleteAccount hands the credential back before forgetting it.
//
// Revoking first is the order the client's own /logout uses, and it is the
// only order that can work: once the row is gone the refresh token is gone
// with it, and there is nothing left to revoke. Forgetting a credential is not
// the same as releasing it — an account removed here used to stay live
// upstream until it aged out on its own.
func (s *Admin) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Best-effort, and deliberately not fatal. The operator asked for this
	// account to be gone; an upstream that is down, slow or has already
	// forgotten the token must not be able to prevent that.
	acct, err := s.store.Account(r.Context(), id)
	if errors.Is(err, store.ErrAccountNotFound) {
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such account")
		return
	}
	if err != nil {
		httpx.Internal(w, s.log, "read the account", err)
		return
	}
	prov, known := s.providers.Find(acct.Provider)
	if tokens, err := s.store.AccountTokens(r.Context(), id); err == nil && known {
		if err := prov.Revoke(r.Context(), s.httpClient, tokens.RefreshToken); err != nil {
			s.log.Warn("could not revoke the refresh token upstream; deleting locally anyway",
				"account", id, "err", err)
		} else {
			s.log.Info("refresh token revoked upstream", "account", id)
		}
	}

	if err := s.store.DeleteAccount(r.Context(), id); errors.Is(err, store.ErrAccountNotFound) {
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such account")
		return
	} else if err != nil {
		httpx.Internal(w, s.log, "delete the account", err)
		return
	}
	s.log.Info("account removed", "account", id, "ip", httpx.ClientIP(r.Context()))
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
