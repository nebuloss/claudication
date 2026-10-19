package openai

import (
	"context"
	"net/http"

	"claudication/internal/oauth"
	"claudication/internal/provider"
)

// Accounts is ChatGPT as the pool, the usage poller and the admin screens see
// it, signing in with a device code (device.go).
type Accounts struct{}

var (
	_ provider.Accounts    = Accounts{}
	_ provider.DeviceLogin = Accounts{}
)

// ID implements provider.Accounts.
func (Accounts) ID() string { return ID }

// Refresh implements provider.Accounts.
func (Accounts) Refresh(ctx context.Context, c *http.Client, refreshToken string) (oauth.Result, error) {
	return Refresh(ctx, c, refreshToken)
}

// Revoke implements provider.Accounts.
func (Accounts) Revoke(ctx context.Context, c *http.Client, refreshToken string) error {
	return Revoke(ctx, c, refreshToken)
}

// Usage implements provider.Accounts with /wham/usage.
func (Accounts) Usage(ctx context.Context, c *http.Client, cred provider.Credential) (provider.Quota, error) {
	return FetchUsage(ctx, c, cred)
}

// Probe implements provider.Accounts.
func (Accounts) Probe(ctx context.Context, c *http.Client, cred provider.Credential, model string) provider.ProbeResult {
	return Probe(ctx, c, cred, model)
}

// ErrorCode implements provider.Accounts.
func (Accounts) ErrorCode(status int, body string) string { return ErrorCode(status, body) }

// Refusal implements provider.Accounts. No ChatGPT refusal is known to say
// something it does not mean, which is what this labels; the policy codes
// (cyber_policy and the rest) say what they mean, in ErrorCode.
func (Accounts) Refusal(string) string { return "" }

// Limits implements provider.Accounts: one row per window, titled by length.
func (Accounts) Limits(detail string) []provider.Limit { return limitRows(detail) }

// StartDevice implements provider.DeviceLogin.
func (Accounts) StartDevice(ctx context.Context, c *http.Client) (oauth.Device, error) {
	return StartDevice(ctx, c)
}

// PollDevice implements provider.DeviceLogin.
func (Accounts) PollDevice(ctx context.Context, c *http.Client, d oauth.Device) (oauth.Result, bool, error) {
	return PollDevice(ctx, c, d)
}
