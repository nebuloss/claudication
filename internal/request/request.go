// Package request is what the gateway knows about a Messages request body
// without decoding it: the few top-level fields it needs, and where in the
// bytes each top-level field sits.
//
// It sits below everything that handles a request — the client dialects, the
// relay and its rewrite passes — so they can share one reading of the body
// instead of each parsing it again, and so a dialect can read a request
// without depending on the relay that sends it.
//
// Why bytes and spans rather than a decoded value: request bodies are
// routinely megabytes (a coding agent resends its whole transcript every
// turn), and decoding one into a generic value costs several times its size.
// Everything here reads the bytes in place, and every rewrite built on it
// copies the body once around the one field it changes.
package request

import "encoding/json"

// Prologue is everything the gateway reads out of a request body before
// relaying it: enough to log what was asked for, choose a timeout, and decide
// whether the attribution block needs adding.
//
// It is read in a single pass, and it names its fields rather than taking the
// whole object, because the field that matters for cost is the one it leaves
// out. `messages` is the bulk of a request — Claude Code resends the whole
// transcript every turn — and decoding into map[string]json.RawMessage copies
// each top-level value, so touching the envelope at all used to duplicate the
// transcript twice per request to read a model name.
type Prologue struct {
	Model  string          `json:"model"`
	Stream bool            `json:"stream"`
	System json.RawMessage `json:"system"`

	// Valid records that the whole body is valid JSON. It is what lets a
	// rewrite splice into the bytes rather than rebuild them: the span
	// functions in this package find structure without checking it.
	Valid bool `json:"-"`
}

// Peek reads the prologue. An unparseable body yields a zero Prologue and is
// left for the upstream to reject.
func Peek(body []byte) Prologue {
	var p Prologue
	p.Valid = json.Unmarshal(body, &p) == nil
	return p
}
