package google

import (
	"context"
	"fmt"
	"net/http"

	"claudication/internal/oauth"
	"claudication/internal/provider"
)

// Accounts is a Google account, as the pool, the usage poller and the admin
// screens see it, signing in through Antigravity by pasting back the address
// consent ends on.
type Accounts struct{}

var (
	_ provider.Accounts   = Accounts{}
	_ provider.PasteLogin = Accounts{}
)

// ID implements provider.Accounts.
func (Accounts) ID() string { return ID }

// LoginURL implements provider.PasteLogin.
func (Accounts) LoginURL(state string, pkce oauth.PKCE, loginHint string) provider.Login {
	return provider.Login{
		URL:      AuthURL(state, pkce, Redirect, loginHint),
		Redirect: Redirect,
		Instructions: "Open the URL and approve access. Your browser then fails to load a localhost:51121 page — " +
			"copy that page's whole address from the address bar and paste it back here.",
	}
}

// Exchange implements provider.PasteLogin: the code redeemed, the account
// named, and its Code Assist project found or made (onboard.go). The project
// is the account's id, as account_uuid stores it; the tier is its plan.
//
// state is not checked here: the attempt the code belongs to was found by the
// state the gateway kept.
func (Accounts) Exchange(ctx context.Context, c *http.Client, code, verifier, _, redirect string) (oauth.Result, error) {
	tr, err := exchangeCode(ctx, c, code, verifier, redirect)
	if err != nil {
		return oauth.Result{}, err
	}
	res := tr.result()
	if res.Email, err = fetchEmail(ctx, c, res.AccessToken); err != nil {
		return oauth.Result{}, err
	}
	id, err := setup(ctx, c, res.AccessToken)
	if err != nil {
		return oauth.Result{}, fmt.Errorf("%s signed in, but %w", res.Email, err)
	}
	res.AccountUUID, res.Plan = id.Project, id.Tier
	return res, nil
}

// Refresh implements provider.Accounts.
func (Accounts) Refresh(ctx context.Context, c *http.Client, refreshToken string) (oauth.Result, error) {
	return Refresh(ctx, c, refreshToken)
}

// Revoke implements provider.Accounts.
func (Accounts) Revoke(ctx context.Context, c *http.Client, refreshToken string) error {
	return Revoke(ctx, c, refreshToken)
}

// Usage implements provider.Accounts with retrieveUserQuota.
func (Accounts) Usage(ctx context.Context, c *http.Client, cred provider.Credential) (provider.Quota, error) {
	return FetchUsage(ctx, c, cred)
}

// Probe implements provider.Accounts.
func (Accounts) Probe(ctx context.Context, c *http.Client, cred provider.Credential, model string) provider.ProbeResult {
	return Probe(ctx, c, cred, model)
}

// ErrorCode implements provider.Accounts.
func (Accounts) ErrorCode(status int, body string) string { return ErrorCode(status, body) }

// Refusal implements provider.Accounts. No Google refusal is known to say
// something it does not mean.
func (Accounts) Refusal(string) string { return "" }

// Limits implements provider.Accounts: one row per model.
func (Accounts) Limits(detail string) []provider.Limit { return limitRows(detail) }
