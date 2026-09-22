package upstream

import (
	"regexp"
	"strings"
)

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

// errorType pulls the upstream's own name for a failure out of its envelope.
//
// The outer type is always "error", which names nothing; the one worth having
// is the inner one. Read with a pattern rather than by unmarshalling, because
// this runs on every recorded request and the body is a string we are not
// otherwise interested in.
var errorType = regexp.MustCompile(`"type"\s*:\s*"([a-z_]+)"`)

// ErrorCode classifies one outcome, in one word, for storing beside it.
//
// One vocabulary, decided once. The request log shows it, its menu lists it
// and its filter matches it — and those were three separate readings of the
// same text before, which is three ways for them to disagree about what a row
// is. Empty means the request worked.
//
// Order is precedence, narrowest first: a content check is also an
// invalid_request_error, and the specific answer is the useful one.
func ErrorCode(status int, body string) string {
	if body == "" {
		return ""
	}
	if ClassifyRefusal(body) == KindContentCheck {
		return string(KindContentCheck)
	}
	// "error" is the envelope, not a kind; anything else is the upstream
	// naming what went wrong.
	for _, m := range errorType.FindAllStringSubmatch(body, -1) {
		if m[1] != "error" {
			return m[1]
		}
	}
	// Nothing came back at all, so there is no upstream word for it: this is
	// the gateway recording that it never got a status.
	if status == 0 {
		return "no_answer"
	}
	return "other"
}
