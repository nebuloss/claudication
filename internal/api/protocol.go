package api

import "net/http"

// Protocol is one client-facing dialect.
//
// Implementations are stateless and shared across requests; per-request state
// belongs to the Exchange that Decode returns.
type Protocol interface {
	// ID is the stable name used in the settings table, the admin UI and the
	// request log. Changing one renames a stored switch, so they are fixed.
	ID() string

	// Title names the surface for a human.
	Title() string

	// Routes are the paths this dialect owns, for the admin UI to show and for
	// an operator to recognise in a log. A surface may own several: the OpenAI
	// one would gain chat completions alongside responses without becoming a
	// second surface.
	Routes() []string

	// Decode reads a caller's request body and returns the exchange that will
	// carry it. An error here is the caller's fault and is answered with
	// WriteError before anything reaches the upstream.
	Decode(body []byte) (Exchange, error)

	// WriteError answers in this dialect's own error envelope, for failures
	// that happen before an Exchange exists or outside one: a body that will
	// not parse, a surface that is switched off, a pool with no accounts. A
	// client can only act on an error it can parse.
	WriteError(w http.ResponseWriter, status int, kind, message string)
}

// Exchange is one request in flight: the Anthropic request to relay, and the
// knowledge of how to turn the answer back into the caller's dialect.
//
// It exists so that knowledge stays with the request that needs it. The OpenAI
// surface, for one, flattens namespaced tools on the way out and has to put
// them back on the way in, and the map that does it belongs to that one
// exchange and to nothing else.
type Exchange interface {
	// Request is the Anthropic request body to send upstream.
	Request() []byte

	// Model and Streaming are what the request asked for, read once here for
	// routing, timeouts and the usage record rather than parsed again by each
	// caller.
	Model() string
	Streaming() bool

	// Headers adjusts, in place, the headers that travel upstream.
	//
	// The request going up is this exchange's, not the caller's, so a header
	// describing the dialect the caller spoke describes nothing there. The
	// identity protocol touches none of them, and that is its contract: every
	// header the client sent is preserved. A translating one drops its own
	// dialect's headers, because the lesson of the refused-request work is
	// that shape is ours to normalise — a request that looks like nothing in
	// particular is the one refused for reasons nobody can see.
	Headers(h http.Header)

	// Sink wraps the caller's writer. The relay writes the upstream answer to
	// it exactly as it would to w; a dialect that must reshape the answer does
	// it here, usually by returning NewReshapingSink.
	Sink(w http.ResponseWriter) Sink
}

// Sink receives the upstream answer on the caller's behalf.
//
// A Sink must implement Unwrap so http.ResponseController can still reach the
// real connection to flush it. Flushing is not optional: a client watches for
// an idle gap and fails the turn when one opens, so a frame sitting in a
// buffer is a frame that never happened.
type Sink interface {
	http.ResponseWriter

	// Unwrap gives http.ResponseController the writer underneath.
	Unwrap() http.ResponseWriter

	// Close is the protocol's last word. It runs whether the relay finished,
	// failed mid-stream, or never wrote a byte, which is what lets a dialect
	// whose stream must end in a terminal event always send one. cause is the
	// error that ended the exchange, or nil.
	Close(cause error)
}
