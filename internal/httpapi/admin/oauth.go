package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/oauth"
	"claudication/internal/provider/anthropic"
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
		body.Provider = anthropic.ID
	}
	if body.Provider != anthropic.ID {
		httpx.WriteError(w, http.StatusBadRequest, "unsupported_provider",
			"only the anthropic provider is implemented so far")
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

	redirectURI := anthropic.RedirectManual

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
	expires := s.pending.StartFor(body.Provider, state, pkce, redirectURI, target.ID)

	out := map[string]any{
		"provider":     body.Provider,
		"state":        state,
		"auth_url":     anthropic.AuthURL(state, pkce, redirectURI, loginHint(target)),
		"redirect_uri": redirectURI,
		"expires_at":   expires.UTC().Format(time.RFC3339),
		"instructions": "Open the URL, approve access, then paste the authorization code Anthropic shows you back here.",
	}
	if target.ID != "" {
		out["account_id"] = target.ID
		out["email"] = target.Email
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// loginHint is the address to pre-fill on the provider's sign-in page, or
// nothing when there is no account or its address was never learned.
func loginHint(a store.Account) string {
	if a.ID == "" || strings.HasPrefix(a.Email, "unknown@") {
		return ""
	}
	return a.Email
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
// account in place. An add whose address is already stored is the same
// renewal, reached the long way round — it used to be the only way, and it
// still must not create a second row. Anything else is a new account, last in
// the priority list.
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
		body.Provider = anthropic.ID
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
	res, err := anthropic.ExchangeCode(r.Context(), s.httpClient,
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

	email := res.Email
	if email == "" {
		email = "unknown@" + body.Provider
	}
	tokens := store.Tokens{AccessToken: res.AccessToken, RefreshToken: res.RefreshToken}
	fresh := store.Account{
		Provider:         body.Provider,
		Email:            email,
		AccountUUID:      res.AccountUUID,
		ExpiresAt:        res.ExpiresAt,
		RefreshExpiresAt: res.RefreshTokenExpiresAt,
	}

	// Which stored account, if any, these tokens renew.
	var target store.Account
	if attempt.Account != "" {
		target, err = s.store.Account(r.Context(), attempt.Account)
		if errors.Is(err, store.ErrAccountNotFound) {
			s.handBack(res.RefreshToken, "the account was removed during its reconnect")
			httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound,
				"that account was removed while you were signing in; add it again instead")
			return
		}
		if err != nil {
			httpx.Internal(w, s.log, "read the account", err)
			return
		}
		if !sameAccount(target, res.AccountUUID, res.Email) {
			s.handBack(res.RefreshToken, "a reconnect came back as a different account")
			s.log.Warn("reconnect approved as a different account; nothing stored",
				"account", target.ID, "expected", target.Email, "approved_as", email)
			httpx.WriteError(w, http.StatusConflict, "wrong_account",
				"access was approved as "+email+", not "+target.Email+
					", so nothing was changed. The browser approves as whichever Claude account "+
					"it is signed in to: sign out of claude.ai there, or open the link in a "+
					"private window, sign in as "+target.Email+", and start the reconnect again.")
			return
		}
	} else if existing, err := s.store.AccountByEmail(r.Context(), body.Provider, email); err == nil {
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

	// Read the subscription usage before answering. The poller would get to it
	// within five minutes, but the operator is looking at the screen now, and a
	// brand-new account showing nothing is exactly the gap worth closing.
	if err := s.poller.Refresh(r.Context(), acct.ID); err != nil {
		s.log.Debug("could not read usage for the new account", "err", err)
	} else if fresh, err := s.store.Account(r.Context(), acct.ID); err == nil {
		acct = fresh
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"account": toAccountJSON(acct), "renewed": renewed})
}

// handBack revokes a refresh token the gateway was issued but will not keep.
//
// Best effort, as on delete: the provider being unreachable must not turn a
// refusal into an error, and an unrevoked grant ages out on its own. But not
// revoking at all would leave a live credential for an account nobody asked
// to connect, held by nothing.
func (s *Admin) handBack(refreshToken, why string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := anthropic.Revoke(ctx, s.httpClient, refreshToken, anthropic.ClientID); err != nil {
		s.log.Warn("could not revoke an unwanted grant; it will lapse on its own", "why", why, "err", err)
	}
}
