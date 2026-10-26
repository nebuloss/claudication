package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/oauth"
	"claudication/internal/provider"
	"claudication/internal/store"
)

// handleOAuthStart begins a consent flow: for a new account, or, with
// account_id, to reconnect one already stored.
//
// A reconnect names the account to the provider (a login hint, as `claude auth
// login --email` sends) and is remembered against it, so completing it can
// check that the operator approved as that account and renew it in place.
func (s *Admin) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider  string `json:"provider"`
		AccountID string `json:"account_id"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if body.Provider == "" {
		body.Provider = s.defaultProvider()
	}
	prov, ok := s.providerFor(w, body.Provider)
	if !ok {
		return
	}

	var target store.Account
	if body.AccountID != "" {
		acct, err := s.store.Account(r.Context(), body.AccountID)
		if errors.Is(err, store.ErrAccountNotFound) {
			httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such account")
			return
		}
		if err != nil {
			httpx.Internal(w, s.log, "read the account", err)
			return
		}
		if acct.Provider != body.Provider {
			httpx.WriteError(w, http.StatusBadRequest, "unsupported_provider",
				"that account belongs to another provider")
			return
		}
		target = acct
	}

	// A provider that signs in with a device code does so: nothing has to
	// come back from the operator's browser, which is what a gateway on
	// another machine needs.
	if device, ok := prov.(provider.DeviceLogin); ok {
		s.startDevice(w, r, body.Provider, device, target)
		return
	}
	paste, ok := prov.(provider.PasteLogin)
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, "unsupported_provider",
			"the "+body.Provider+" provider has no sign-in this gateway can run")
		return
	}

	pkce, err := oauth.NewPKCE()
	if err != nil {
		httpx.Internal(w, s.log, "start the sign-in", err)
		return
	}
	state, err := oauth.NewState()
	if err != nil {
		httpx.Internal(w, s.log, "start the sign-in", err)
		return
	}
	login := paste.LoginURL(state, pkce, loginHint(target))
	expires := s.pending.StartFor(body.Provider, state, pkce, login.Redirect, target.ID)

	out := map[string]any{
		"provider":     body.Provider,
		"flow":         "paste",
		"state":        state,
		"auth_url":     login.URL,
		"redirect_uri": login.Redirect,
		"expires_at":   expires.UTC().Format(time.RFC3339),
		"instructions": login.Instructions,
	}
	if target.ID != "" {
		out["account_id"] = target.ID
		out["email"] = target.Email
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// startDevice begins a device-code sign-in: the provider hands out a short
// code, the operator enters it on the provider's own page wherever they are
// signed in, and the UI asks /oauth/poll until it is approved.
func (s *Admin) startDevice(w http.ResponseWriter, r *http.Request, providerID string,
	device provider.DeviceLogin, target store.Account) {
	d, err := device.StartDevice(r.Context(), s.httpClient)
	if err != nil {
		s.log.Warn("could not start a device sign-in", "provider", providerID, "err", err)
		httpx.WriteError(w, http.StatusBadGateway, "exchange_failed", err.Error())
		return
	}
	state, err := oauth.NewState()
	if err != nil {
		httpx.Internal(w, s.log, "start the sign-in", err)
		return
	}
	expires := s.pending.StartDevice(providerID, state, d, target.ID)
	out := map[string]any{
		"provider":         providerID,
		"flow":             "device",
		"state":            state,
		"user_code":        d.UserCode,
		"verification_uri": d.VerificationURI,
		"interval_s":       int(d.Interval.Seconds()),
		"expires_at":       expires.UTC().Format(time.RFC3339),
		"instructions": "Open the link, sign in to the account to connect, and enter the code. " +
			"This page notices when you have.",
	}
	if target.ID != "" {
		out["account_id"] = target.ID
		out["email"] = target.Email
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleOAuthPoll asks the provider once whether a device code has been
// approved: 202 while it has not, and once it has, the account stored as a
// pasted code's would be, with the same answer.
//
// One question per call, made by the UI at the code's interval, rather than a
// request held open until the operator gets round to it: a held request is a
// connection a proxy may cut, and asking at the interval is what the provider
// asks for.
func (s *Admin) handleOAuthPoll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string `json:"provider"`
		State    string `json:"state"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	prov, ok := s.providerFor(w, body.Provider)
	if !ok {
		return
	}
	device, ok := prov.(provider.DeviceLogin)
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, "unsupported_provider",
			"the "+body.Provider+" provider does not sign in with a device code")
		return
	}
	attempt, err := s.pending.Peek(body.Provider, body.State)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "unknown_state", err.Error())
		return
	}
	if attempt.Device.ID == "" {
		httpx.WriteError(w, http.StatusBadRequest, "unknown_state", "that sign-in is not a device-code one")
		return
	}
	res, pending, err := device.PollDevice(r.Context(), s.httpClient, attempt.Device)
	switch {
	case errors.Is(err, oauth.ErrDeviceSpent):
		// Expired, or approved and then not redeemed: asking again cannot
		// finish it, so the attempt ends here and the UI offers a new one.
		s.pending.Consume(body.State)
		s.log.Warn("device sign-in ended", "provider", body.Provider, "err", err)
		httpx.WriteError(w, http.StatusBadGateway, "sign_in_failed", err.Error())
		return
	case err != nil:
		// Before any approval: the network, or the endpoint having a bad
		// minute. The attempt stays, and the UI asks again at the interval.
		s.log.Warn("device sign-in poll failed", "provider", body.Provider, "err", err)
		httpx.WriteError(w, http.StatusBadGateway, "exchange_failed", err.Error())
		return
	case pending:
		httpx.WriteJSON(w, http.StatusAccepted, map[string]any{
			"status":     "pending",
			"interval_s": int(attempt.Device.Interval.Seconds()),
		})
		return
	}
	s.pending.Consume(body.State)
	s.storeConsent(w, r, prov, attempt, res)
}

// loginHint is the address to pre-fill on the provider's sign-in page, or
// nothing when there is no account or its address was never learned.
func loginHint(a store.Account) string {
	if a.ID == "" || strings.HasPrefix(a.Email, "unknown@") {
		return ""
	}
	return a.Email
}

// identity is the provider's id for who signed in, or "" for a provider whose
// stored id says no such thing (provider.AddressIdentity), which is then
// known by address alone.
func identity(prov provider.Accounts, res oauth.Result) string {
	if _, byAddress := prov.(provider.AddressIdentity); byAddress {
		return ""
	}
	return res.AccountUUID
}

// sameAccount reports whether a consent flow's tokens belong to a stored
// account: by the provider's account id where both sides have one, which
// survives a change of address, and by address otherwise.
func sameAccount(a store.Account, uuid, email string) bool {
	if a.AccountUUID != "" && uuid != "" {
		return a.AccountUUID == uuid
	}
	return email != "" && strings.EqualFold(a.Email, email)
}

// handleOAuthComplete redeems the code and stores what it bought.
//
// Three outcomes. A reconnect whose tokens belong to its account renews that
// account in place. An add of an account already stored — the same provider
// account id, or the same address where no id was ever stored — is the same
// renewal, reached the long way round; it used to be the only way, and it
// still must not create a second row. Anything else is a new account, last in
// the priority list: the same address with a different account id is a
// different account (one ChatGPT sign-in, a personal and a Team workspace).
//
// A reconnect that comes back as someone else is refused, and the tokens are
// handed back to the provider rather than kept. The browser decides who
// approves, not the gateway: one already signed in to another Claude account
// approves as that one whatever the login hint said, and storing the result
// would either add an account nobody asked for or, worse, put one person's
// subscription behind another's name.
func (s *Admin) handleOAuthComplete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider    string `json:"provider"`
		State       string `json:"state"`
		RedirectURL string `json:"redirect_url"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if body.Provider == "" {
		body.Provider = s.defaultProvider()
	}
	prov, ok := s.providerFor(w, body.Provider)
	if !ok {
		return
	}
	paste, ok := prov.(provider.PasteLogin)
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, "unsupported_provider",
			"the "+body.Provider+" provider signs in another way")
		return
	}

	code, returnedState, err := oauth.ParseCallback(body.RedirectURL)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_callback", err.Error())
		return
	}
	// Trust the state we issued; only reject when the callback carries a
	// different one, since some responses omit it entirely.
	if returnedState != "" && returnedState != body.State {
		httpx.WriteError(w, http.StatusBadRequest, "state_mismatch",
			"that response belongs to a different login attempt")
		return
	}

	attempt, err := s.pending.Peek(body.Provider, body.State)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "unknown_state", err.Error())
		return
	}

	// Replay the redirect the authorize request carried; a mismatch is
	// rejected by the token endpoint.
	res, err := paste.Exchange(r.Context(), s.httpClient,
		code, attempt.PKCE.Verifier, body.State, attempt.RedirectURI)
	if err != nil {
		// The attempt stays live, so a corrected paste can be retried without
		// walking through the consent screen again.
		s.log.Warn("token exchange failed", "err", err)
		httpx.WriteError(w, http.StatusBadGateway, "exchange_failed", err.Error())
		return
	}
	// Redeemed: the verifier is spent from here on.
	s.pending.Consume(body.State)
	s.storeConsent(w, r, prov, attempt, res)
}

// storeConsent stores what a finished sign-in bought, however it was signed in
// — a pasted code or an approved device code — and answers with the account.
func (s *Admin) storeConsent(w http.ResponseWriter, r *http.Request, prov provider.Accounts, attempt oauth.Attempt, res oauth.Result) {
	providerID := prov.ID()
	email := res.Email
	if email == "" {
		email = "unknown@" + providerID
	}
	tokens := store.Tokens{AccessToken: res.AccessToken, RefreshToken: res.RefreshToken}
	fresh := store.Account{
		Provider:         providerID,
		Email:            email,
		AccountUUID:      res.AccountUUID,
		Plan:             res.Plan,
		ExpiresAt:        res.ExpiresAt,
		RefreshExpiresAt: res.RefreshTokenExpiresAt,
	}
	var err error

	// Which stored account, if any, these tokens renew.
	var target store.Account
	if attempt.Account != "" {
		target, err = s.store.Account(r.Context(), attempt.Account)
		if errors.Is(err, store.ErrAccountNotFound) {
			s.handBack(prov, res.RefreshToken, "the account was removed during its reconnect")
			httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound,
				"that account was removed while you were signing in; add it again instead")
			return
		}
		if err != nil {
			httpx.Internal(w, s.log, "read the account", err)
			return
		}
		if !sameAccount(target, identity(prov, res), res.Email) {
			s.handBack(prov, res.RefreshToken, "a reconnect came back as a different account")
			s.log.Warn("reconnect approved as a different account; nothing stored",
				"account", target.ID, "expected", target.Email, "approved_as", email)
			httpx.WriteError(w, http.StatusConflict, "wrong_account",
				"access was approved as "+email+", not "+target.Email+
					", so nothing was changed. The browser approves as whichever account "+
					"it is signed in to: sign out there, or open the link in a "+
					"private window, sign in as "+target.Email+", and start the reconnect again.")
			return
		}
	} else if existing, err := s.store.AccountByIdentity(r.Context(), providerID, identity(prov, res), email); err == nil {
		target = existing
	}

	var acct store.Account
	renewed := target.ID != ""
	if renewed {
		acct, err = s.store.RenewAccount(r.Context(), target.ID, fresh, tokens)
	} else {
		acct, err = s.store.UpsertAccount(r.Context(), fresh, tokens)
	}
	if err != nil {
		s.log.Error("store account", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not store the account")
		return
	}

	if renewed {
		// The backoff belonged to the credentials just replaced.
		s.pool.Reset(acct.ID)
		s.log.Info("account reconnected", "provider", acct.Provider, "email", acct.Email,
			"account", acct.ID)
	} else {
		s.log.Info("account authorised", "provider", acct.Provider, "email", acct.Email)
	}

	// Try every model it lists, in the background: which work there is what
	// its Models page shows, and what requests are routed by.
	s.availability.Scan(acct.ID)

	// Read the subscription usage before answering. The poller would get to it
	// within five minutes, but the operator is looking at the screen now, and a
	// brand-new account showing nothing is exactly the gap worth closing.
	if err := s.poller.Refresh(r.Context(), acct.ID); err != nil {
		s.log.Debug("could not read usage for the new account", "err", err)
	} else if fresh, err := s.store.Account(r.Context(), acct.ID); err == nil {
		acct = fresh
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"account": s.accountJSON(acct), "renewed": renewed})
}

// handBack revokes a refresh token the gateway was issued but will not keep.
//
// Best effort, as on delete: the provider being unreachable must not turn a
// refusal into an error, and an unrevoked grant ages out on its own. But not
// revoking at all would leave a live credential for an account nobody asked
// to connect, held by nothing.
func (s *Admin) handBack(prov provider.Accounts, refreshToken, why string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := prov.Revoke(ctx, s.httpClient, refreshToken); err != nil {
		s.log.Warn("could not revoke an unwanted grant; it will lapse on its own", "why", why, "err", err)
	}
}
