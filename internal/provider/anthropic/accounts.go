package anthropic

import (
	"context"
	"net/http"

	"claudication/internal/oauth"
	"claudication/internal/provider"
)

// Accounts is Anthropic as the pool, the usage poller and the admin screens
// see it: provider.Accounts over the functions this package already had, and
// provider.PasteLogin for the consent flow — Anthropic shows the code on a
// page of its own, which is pasted back.
type Accounts struct{}

var (
	_ provider.Accounts   = Accounts{}
	_ provider.PasteLogin = Accounts{}
)

// ID implements provider.Accounts.
func (Accounts) ID() string { return ID }

// Refresh implements provider.Accounts.
func (Accounts) Refresh(ctx context.Context, c *http.Client, refreshToken string) (oauth.Result, error) {
	return Refresh(ctx, c, refreshToken)
}

// Revoke implements provider.Accounts, as the client's own /logout does.
func (Accounts) Revoke(ctx context.Context, c *http.Client, refreshToken string) error {
	return Revoke(ctx, c, refreshToken, ClientID)
}

// Usage implements provider.Accounts with /api/oauth/usage.
func (Accounts) Usage(ctx context.Context, c *http.Client, cred provider.Credential) (provider.Quota, error) {
	u, err := FetchUsage(ctx, c, cred.Token)
	if err != nil {
		return provider.Quota{}, err
	}
	return u.Usage(), nil
}

// Probe implements provider.Accounts.
func (Accounts) Probe(ctx context.Context, c *http.Client, cred provider.Credential, model string) provider.ProbeResult {
	return Probe(ctx, c, cred.Token, model)
}

// ErrorCode implements provider.Accounts.
func (Accounts) ErrorCode(status int, body string) string { return ErrorCode(status, body) }

// Refusal implements provider.Accounts: the content check, which reads as a
// billing problem and is not one.
func (Accounts) Refusal(body string) string { return string(ClassifyRefusal(body)) }

// Limits implements provider.Accounts: the limits array /usage prints.
func (Accounts) Limits(detail string) []provider.Limit { return limitRows(detail) }

// LoginURL implements provider.PasteLogin with the client's manual redirect,
// the page that shows the code to paste.
func (Accounts) LoginURL(state string, pkce oauth.PKCE, loginHint string) provider.Login {
	return provider.Login{
		URL:          AuthURL(state, pkce, RedirectManual, loginHint),
		Redirect:     RedirectManual,
		Instructions: "Open the URL, approve access, then paste the authorization code Anthropic shows you back here.",
	}
}

// Exchange implements provider.PasteLogin.
func (Accounts) Exchange(ctx context.Context, c *http.Client, code, verifier, state, redirect string) (oauth.Result, error) {
	return ExchangeCode(ctx, c, code, verifier, state, redirect)
}
