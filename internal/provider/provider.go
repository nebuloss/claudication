// Package provider is what the gateway needs to know about the services that
// answer its requests, and nothing more.
//
// The relay's job — pick an account, send, fail over, hold a stalled stream,
// copy the answer back byte for byte — is the same whoever answers. What
// differs is the address, how a request is authenticated, and how usage,
// quota and errors are read back out of an answer. Those sit behind [Wire].
// What differs about the accounts themselves — signing one in, refreshing its
// token, asking what it has left, testing it — sits behind [Accounts].
//
// Each provider lives in a package of its own under this one, and is the only
// place its URLs, headers, event names and error shapes appear:
//
//	internal/provider/anthropic  a Claude subscription, over Anthropic Messages
//	internal/provider/openai     a ChatGPT subscription, over the Codex
//	                             backend's Responses API
//	internal/provider/google     a Google account's Gemini, over the Code
//	                             Assist backend gemini-cli signs in to
//
// A new provider is a new package implementing both, wired in by the HTTP
// layer. Nothing in the relay changes: the gateway runs one relay per
// provider, each with its own wire and its own passes.
package provider

import (
	"net/http"
	"time"
)

// Usage is what one request cost, in one vocabulary whoever answered.
//
// The vocabulary is Anthropic's, because the budgets and reports were written
// in it first: InputTokens excludes what was read from the prompt cache. A
// provider that counts cached input inside its input figure — OpenAI does —
// normalises in its Meter, so a budget means the same thing on either.
type Usage struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
	// ReasoningTokens is the part of OutputTokens the model spent reasoning,
	// where the provider reports it apart (OpenAI does; Anthropic folds
	// thinking into output without saying how much). Already inside
	// OutputTokens: kept to be shown, never to be added.
	ReasoningTokens int
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

// Credential is what an account authenticates a request with.
type Credential struct {
	// Token is the account's current access token.
	Token string
	// AccountID is the provider's own id for the account, as stored in
	// account_uuid. Anthropic does not need it on a request; ChatGPT's
	// backend wants it on every one, because one sign-in can reach several
	// workspaces and the token alone does not say which is being billed.
	AccountID string
}

// Wire is the relay's view of a provider.
type Wire interface {
	// ID names the provider as accounts are stored under it and as the pool
	// is asked for them.
	ID() string

	// BaseURL is where requests go, without a trailing slash.
	BaseURL() string

	// Authorize puts an account's credential on an outgoing request, along
	// with whatever else the provider requires of every authenticated call.
	// The client's own headers are already there; Authorize adds to them and
	// must not remove the caller's capabilities.
	Authorize(out *http.Request, c Credential)

	// Meter returns a reader for an answer of the given content type.
	Meter(contentType string, into Reading) Meter

	// Event classifies a streamed event by name. The relay holds a stream's
	// opening back until it is flowing, and retries one that stalls before
	// then; which events mean what is the provider's to say.
	Event(name []byte) EventKind

	// Inspect reads what an answer's status line and headers say about the
	// account that served it, before its body is relayed. body is the error
	// body when the relay has already read one to decide on a retry, and nil
	// otherwise. The zero Verdict is "nothing learned", which is what a
	// provider that reports nothing there returns.
	Inspect(status int, h http.Header, body []byte) Verdict

	// ModelsRequest is how one account's model list is asked for, on BaseURL:
	// the method, the path — with the caller's query when the provider takes
	// one — and the body, nil for none. A request rather than a path because
	// not every upstream lists its models with a GET: Code Assist has no list
	// and is asked its quota, a POST naming the account's project.
	ModelsRequest(rawQuery string, c Credential) (method, path string, body []byte)

	// ModelList turns that answer into the gateway's model list shape —
	// {"data":[{"id","type":"model","display_name",...}]}, Anthropic's — so
	// the catalogue reads every provider's list one way. A provider whose
	// list already has that shape returns it as it came.
	ModelList(body []byte) ([]byte, error)
}

// Stamper is a provider whose requests name the account in the body as well
// as in the headers: Code Assist bills the Cloud project the body names, and
// the project is per account. The relay stamps each attempt's body after it
// has leased the account, from the same unstamped bytes every time, so a
// retry on another account names that account.
//
// Only for a provider no client speaks as it is, whose every request body is
// the gateway's own translation: stamping is a change to the bytes, and the
// relay's rule is that a client's bytes go upstream unchanged.
type Stamper interface {
	Stamp(body []byte, c Credential) []byte
}

// Verdict is what one answer said about its account.
type Verdict struct {
	// Kind, when set, is the pool's failure kind for this answer in place of
	// the one its status implies: a 429 that says the plan does not include
	// the service at all is not a rate limit to wait out.
	Kind string
	// Until is when the upstream said the account can serve again — a usage
	// limit's reset. Zero leaves the pool's own backoff to decide.
	Until time.Time
	// Quota is the account's windows as the answer's headers reported them,
	// or nil when they reported none.
	Quota *Quota
}

// Quota is what a provider reports about an account's subscription windows.
//
// Windows are reported by their length rather than by name, because the
// names are the provider's and its positions are not a promise: ChatGPT's
// "primary" has been the 5-hour window on every plan measured, but nothing
// says it must be. The store maps them onto the columns it has by length.
type Quota struct {
	Windows []Window
	// Limited is the provider saying the account may not serve now, whatever
	// the windows' figures read.
	Limited bool
	// Detail is the provider's own rows, as its Accounts.Limits reads them
	// back for the accounts screen.
	Detail string
	// At is when the figures were read.
	At time.Time
}

// Window is one rolling limit.
type Window struct {
	// Seconds is the window's length.
	Seconds int64
	// UsedPercent is how much of it is spent, 0-100.
	UsedPercent float64
	// ResetAt is when it starts afresh; zero when not said.
	ResetAt time.Time
	// Status is the provider's word for the window's state, "allowed" or a
	// reason it is closed; empty when it said nothing.
	Status string
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
