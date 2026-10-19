package provider

import (
	"context"
	"net/http"

	"claudication/internal/oauth"
)

// Accounts is what the gateway does with a provider's accounts outside a
// relayed request: keep their tokens alive, ask what they have left, test
// them, hand them back, and name their failures.
//
// The pool, the usage poller, the recorder and the admin screens all reach a
// provider through this, handed to them by the composition root, so none of
// them names one. Signing an account in is the one part that differs in kind
// rather than in detail, and is a second interface: see [PasteLogin] and
// [DeviceLogin].
type Accounts interface {
	// ID is the provider, as accounts are stored under it.
	ID() string

	// Refresh trades a refresh token for new tokens. A refusal that will
	// never come right on its own wraps oauth.ErrInvalidGrant.
	Refresh(ctx context.Context, c *http.Client, refreshToken string) (oauth.Result, error)

	// Revoke hands a refresh token back. Best effort: callers log a failure
	// and carry on.
	Revoke(ctx context.Context, c *http.Client, refreshToken string) error

	// Usage asks what the account's subscription has left.
	Usage(ctx context.Context, c *http.Client, cred Credential) (Quota, error)

	// Probe sends the smallest useful request, to prove the credential
	// serves end to end. model empty is the provider's own choice.
	Probe(ctx context.Context, c *http.Client, cred Credential, model string) ProbeResult

	// ErrorCode names one recorded failure in one word, in the provider's
	// vocabulary; empty for a request that worked.
	ErrorCode(status int, body string) string

	// Refusal names what an error body really means when that is not what
	// it says — Anthropic's content check, which reads as billing — or "".
	Refusal(body string) string

	// Limits reads a stored Quota.Detail back as rows for the accounts
	// screen, each titled the way the provider's own client titles it.
	Limits(detail string) []Limit
}

// Limit is one row of an account's usage, as the accounts screen shows it.
type Limit struct {
	Title    string  `json:"title"`
	Kind     string  `json:"kind"`
	Percent  float64 `json:"percent"`
	Severity string  `json:"severity"`
	ResetsAt string  `json:"resets_at,omitempty"`
	IsActive bool    `json:"is_active"`
	// WindowSeconds is the window's length, when the provider reports one,
	// so the screen can label a gauge from it rather than from a position.
	WindowSeconds int64 `json:"window_seconds,omitempty"`
}

// ProbeResult is what a credential test learned.
type ProbeResult struct {
	OK           bool   `json:"ok"`
	Model        string `json:"model,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
	InputTokens  int    `json:"input_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
	Reply        string `json:"reply,omitempty"`
	LatencyMS    int64  `json:"latency_ms"`
	Status       int    `json:"status"`
	RequestID    string `json:"request_id,omitempty"`
	Error        string `json:"error,omitempty"`
}

// PasteLogin is a provider whose accounts sign in by consenting in a browser
// and pasting back what the provider shows: a code, or the address the
// browser was sent to.
type PasteLogin interface {
	// LoginURL is the consent page for one attempt. loginHint, when set,
	// names the account to sign in as.
	LoginURL(state string, pkce oauth.PKCE, loginHint string) Login
	// Exchange redeems what was pasted back. redirect is the Login's own,
	// which the exchange must replay.
	Exchange(ctx context.Context, c *http.Client, code, verifier, state, redirect string) (oauth.Result, error)
}

// Login is one consent attempt, as the operator is shown it.
type Login struct {
	URL          string
	Redirect     string
	Instructions string
}

// DeviceLogin is a provider whose accounts sign in with a device code: the
// gateway is given a short code, the operator enters it on the provider's own
// page wherever they are signed in, and the gateway asks until it is
// approved. Nothing has to reach the gateway from the browser, which is what
// makes it work for a gateway on another machine.
type DeviceLogin interface {
	// StartDevice asks for a code.
	StartDevice(ctx context.Context, c *http.Client) (oauth.Device, error)
	// PollDevice asks whether the code has been approved. pending is true
	// while it has not, and is not an error.
	PollDevice(ctx context.Context, c *http.Client, d oauth.Device) (res oauth.Result, pending bool, err error)
}

// Registry is every provider the gateway serves, in the order the admin
// screens list them.
type Registry []Accounts

// Find is the provider with the given id.
func (r Registry) Find(id string) (Accounts, bool) {
	for _, a := range r {
		if a.ID() == id {
			return a, true
		}
	}
	return nil, false
}
