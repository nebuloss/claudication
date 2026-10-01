// Package provider is what the relay needs to know about the service that
// answers its requests, and nothing more.
//
// The relay's job — pick an account, send, fail over, hold a stalled stream,
// copy the answer back byte for byte — is the same whoever answers. What
// differs is the address, how a request is authenticated, and how usage and
// errors are read back out of an answer. Those sit behind [Wire], and each
// provider lives in a package of its own under this one: internal/provider/
// anthropic is the only one today, and the only place Anthropic's URLs,
// headers, event names and error shapes appear.
//
// A new provider is a new package implementing Wire (plus whatever its
// accounts need: a token refresh for the pool, a usage endpoint for the admin
// screens), wired in by the HTTP layer. Nothing in the relay changes.
package provider

import (
	"net/http"
	"time"
)

// Usage is what one request cost, as the provider reported it.
type Usage struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
}

// Meter reads a relayed answer as it goes past, for usage and failures.
//
// Feed is given every chunk after it has been written to the client, and must
// never alter or hold up the stream: what it learns is bookkeeping, and
// nothing it learns is worth breaking a response over.
type Meter interface {
	Feed(chunk []byte)
	Done()
}

// Reading is where a Meter writes what it learns.
type Reading struct {
	Usage *Usage
	// StreamError is set when the answer reported a failure after its status
	// line — a mid-stream error, which a status code alone would record as a
	// success.
	StreamError *string
	// FirstContent receives the moment the first event carrying content went
	// past: the time to first token.
	FirstContent *time.Time
}

// Wire is the relay's view of a provider.
type Wire interface {
	// BaseURL is where requests go, without a trailing slash.
	BaseURL() string

	// Authorize puts an account's credential on an outgoing request, along
	// with whatever else the provider requires of every authenticated call.
	// The client's own headers are already there; Authorize adds to them and
	// must not remove the caller's capabilities.
	Authorize(out *http.Request, token string)

	// Meter returns a reader for an answer of the given content type.
	Meter(contentType string, into Reading) Meter

	// Event classifies a streamed event by name. The relay holds a stream's
	// opening back until it is flowing, and retries one that stalls before
	// then; which events mean what is the provider's to say.
	Event(name []byte) EventKind
}

// EventKind is what a streamed event means for whether the stream is alive.
type EventKind uint8

const (
	// EventContent carries something the model produced.
	EventContent EventKind = iota
	// EventQuiet carries nothing yet: the stream opening, a keep-alive.
	EventQuiet
	// EventSettling finishes a block or the answer.
	EventSettling
	// EventError is a failure reported inside the stream.
	EventError
)
