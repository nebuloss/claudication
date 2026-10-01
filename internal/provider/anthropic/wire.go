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
	"net/http"
	"strings"

	"claudication/internal/provider"
)

// BaseURL is where requests go.
const BaseURL = "https://api.anthropic.com"

// oauthBeta must reach the upstream on every subscription-authenticated
// request. The gateway contract is explicit that stripping it fails those
// requests with a 401, so it is merged into whatever the client sent rather
// than replacing it.
const oauthBeta = "oauth-2025-04-20"

// Provider is Anthropic, as the relay sees it.
type Provider struct{}

var _ provider.Wire = Provider{}

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
		out.Header.Set("anthropic-version", "2023-06-01")
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
	case string(name) == "error":
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
	case "content_block_stop", "message_delta", "message_stop":
		return true
	}
	return false
}

// quietEvent reports whether an SSE event name carries no content: the ones
// the upstream sends before it has produced anything.
func quietEvent(name []byte) bool {
	switch string(name) {
	case "message_start", "content_block_start", "ping":
		return true
	}
	return false
}
