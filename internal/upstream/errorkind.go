package upstream

import "strings"

// ErrorKind names an upstream refusal that is worth explaining rather than
// only relaying.
//
// The relay never changes what the upstream said — the client reads that text
// and decides what to do with it. This is for the other reader: the operator
// looking at the request log afterwards, asking why it failed.
type ErrorKind string

const (
	// KindContentCheck is the refusal that reads like a billing problem and
	// is not one:
	//
	//	"Third-party apps now draw from your extra usage, not your plan
	//	 limits. Add more at claude.ai/settings/usage and keep going."
	//
	// It is emitted when the request's *content* is classified as coming from
	// a third-party app rather than from Claude Code. Adding usage credit does
	// not fix it, and the same key succeeds on the next request with slightly
	// different content — which is exactly why it reads as a flaky quota
	// problem. Two triggers have been found and are normalised on the way out
	// (see mcpnames.go and systemtext.go); this label exists because there
	// will be others, and because the message will still be about billing when
	// there are.
	//
	// Worth knowing: Claude Code itself has no handling for this string
	// anywhere in its bundle. The classification is entirely server-side and
	// the first-party client never encounters it, so there is no upstream
	// behaviour to copy — the explanation has to come from here.
	KindContentCheck ErrorKind = "content_check"
)

// ClassifyRefusal names what an upstream error body actually means, or "" when
// it is what it says it is.
//
// Matched on the wording rather than the status because the status is 400 —
// shared with every genuinely malformed request — and the error type is
// invalid_request_error, shared with all of them too. The message is the only
// part that distinguishes it.
func ClassifyRefusal(body string) ErrorKind {
	// Both halves, so an unrelated message that happens to mention extra usage
	// is not relabelled.
	if strings.Contains(body, "Third-party apps") && strings.Contains(body, "extra usage") {
		return KindContentCheck
	}
	return ""
}
