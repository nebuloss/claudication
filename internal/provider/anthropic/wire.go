// Package anthropic is the Anthropic API as reached with a Claude subscription:
// where requests go and how they are authenticated, how usage, errors and
// refusals are read back out of answers, the OAuth client that connects an
// account, and the account endpoints — test probe, subscription usage —
// behind the admin screens.
//
// It is the only package that knows any of those details. The relay sees it
// through provider.Wire; the pool through a refresh function; the admin
// screens call it directly, because they are about Anthropic accounts.
package anthropic

import (
	"context"
	"io"
	"net/http"
	"strings"

	"claudication/internal/anthropicwire"
	"claudication/internal/provider"
)

// BaseURL is where requests go.
const BaseURL = "https://api.anthropic.com"

// oauthBeta must reach the upstream on every subscription-authenticated
// request. The gateway contract is explicit that stripping it fails those
// requests with a 401, so it is merged into whatever the client sent rather
// than replacing it.
const oauthBeta = "oauth-2025-04-20"

// apiVersion is the anthropic-version a request carries when its caller named
// none.
const apiVersion = "2023-06-01"

// newAccountRequest is a request of the gateway's own to path on BaseURL,
// authorised as one account: its bearer token and the OAuth capability every
// such request needs. For the account endpoints the gateway calls itself —
// the test probe, the subscription usage — which used to set the same two
// headers by hand, each with its own copy of the beta.
func newAccountRequest(ctx context.Context, method, path string, body io.Reader, token string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", oauthBeta)
	return req, nil
}

// clip shortens an upstream's text for an error message, marking the cut.
func clip(s string, max int) string {
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// Provider is Anthropic, as the relay sees it.
type Provider struct{}

var _ provider.Wire = Provider{}

// ID is the provider name accounts are stored under.
const ID = "anthropic"

// ID implements provider.Wire.
func (Provider) ID() string { return ID }

// BaseURL implements provider.Wire.
func (Provider) BaseURL() string { return BaseURL }

// Authorize implements provider.Wire: the account's bearer token, the OAuth
// capability the upstream requires of it, and an API version if the caller
// named none.
func (Provider) Authorize(out *http.Request, token string) {
	out.Header.Set("Authorization", "Bearer "+token)

	// Merge rather than replace: the client's beta values are its own
	// capabilities and the contract forbids allowlisting them.
	betas := out.Header.Get("anthropic-beta")
	if !hasBeta(betas, oauthBeta) {
		if betas == "" {
			betas = oauthBeta
		} else {
			betas = oauthBeta + "," + betas
		}
		out.Header.Set("anthropic-beta", betas)
	}
	if out.Header.Get("anthropic-version") == "" {
		out.Header.Set("anthropic-version", apiVersion)
	}
}

func hasBeta(header, want string) bool {
	for _, part := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(part), want) {
			return true
		}
	}
	return false
}

// Meter implements provider.Wire. Which reader can recover the usage depends
// on the shape of the answer, not on what the caller asked for: a request
// with "stream": true that fails before the stream starts comes back as plain
// JSON.
func (Provider) Meter(contentType string, into provider.Reading) provider.Meter {
	if strings.Contains(contentType, "text/event-stream") {
		s := newSSEScanner(into.Usage, into.StreamError)
		s.firstContent = into.FirstContent
		return s
	}
	return newJSONUsage(into.Usage)
}

// Event implements provider.Wire for the Messages stream.
func (Provider) Event(name []byte) provider.EventKind {
	switch {
	case quietEvent(name):
		return provider.EventQuiet
	case string(name) == anthropicwire.EventError:
		return provider.EventError
	case settlingEvent(name):
		return provider.EventSettling
	}
	return provider.EventContent
}

// settlingEvent reports whether an event finishes a block or the answer: a
// stream that has got this far is not stalled at its start.
func settlingEvent(name []byte) bool {
	switch string(name) {
	case anthropicwire.EventContentBlockStop, anthropicwire.EventMessageDelta, anthropicwire.EventMessageStop:
		return true
	}
	return false
}

// quietEvent reports whether an SSE event name carries no content: the ones
// the upstream sends before it has produced anything.
func quietEvent(name []byte) bool {
	switch string(name) {
	case anthropicwire.EventMessageStart, anthropicwire.EventContentBlockStart, anthropicwire.EventPing:
		return true
	}
	return false
}
