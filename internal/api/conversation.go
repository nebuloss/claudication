package api

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Identifying the chat a request belongs to.
//
// Every client measured sends something that names its conversation, and every
// one of them sends it in a header. That is the whole reason this file is
// small and reads nothing out of the messages: a gateway that had to fingerprint
// message content to group requests would be storing conversation content, and
// it does not have to.
//
// What was measured, by pointing each client at a capture stub and reading the
// wire (2026-09-17):
//
//	client       what it sends                                    where
//	-----------  ----------------------------------------------   ---------
//	Claude Code  X-Claude-Code-Session-Id: <uuid>                  header
//	             metadata.user_id: "{…,\"session_id\":\"<uuid>\"}" body, same uuid
//	opencode     x-session-id / x-session-affinity: ses_<id>       header
//	crush        x-session-id / x-session-affinity: <hash>         header
//	Codex        session_id / conversation_id: <uuid>              header
//
// Claude Code sends it twice, and the header is preferred because reading it
// costs nothing. The body is kept as a fallback for a client old enough to
// predate the header; it is a single pass that names one field, for the reason
// upstream.Peek gives.
//
// Note what is deliberately NOT used: a bare metadata.user_id that is not this
// JSON object. That names a *user*, stable across every chat they ever have,
// so treating it as a conversation would collapse a person's whole history
// into one row — worse than having no grouping at all.

// maxIdentifier bounds anything client-controlled before it reaches the
// database or the admin UI. The longest real id measured is 36 characters (a
// UUID); opencode's is 28. A client is free to send a megabyte, and a column
// that accepts one is a way to fill someone's disk one request at a time.
const maxIdentifier = 96

// HeaderID returns the first of names the request carries, cleaned.
//
// Order is the caller's, and it matters: a dialect lists its most specific
// header first, so a client sending both a generic and a specific one is
// identified by the one that says more.
func HeaderID(h http.Header, names ...string) string {
	for _, name := range names {
		if v := CleanIdentifier(h.Get(name)); v != "" {
			return v
		}
	}
	return ""
}

// CleanIdentifier makes a client-supplied string safe to store and to print.
//
// Control characters go because this value is rendered in a terminal log line
// and in the admin UI, and an id containing an escape sequence is an id that
// can rewrite what an operator sees. Length is bounded for the reason above.
// The result is not escaped for HTML — that is the UI's job, and doing it here
// would put entities in the database.
func CleanIdentifier(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	v = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, v)
	if len(v) > maxIdentifier {
		v = v[:maxIdentifier]
	}
	return v
}

// metadataEnvelope is the one field of a request body this package reads.
//
// Named rather than taken whole, exactly as upstream.Prologue is: `messages`
// is the bulk of a request and decoding into map[string]json.RawMessage would
// copy the transcript to read a session id.
type metadataEnvelope struct {
	Metadata struct {
		UserID string `json:"user_id"`
	} `json:"metadata"`
}

// SessionFromMetadata reads Claude Code's session id out of metadata.user_id.
//
// The field is a JSON *string* whose contents are themselves JSON, which is why
// this unmarshals twice:
//
//	"metadata": {"user_id": "{\"device_id\":\"0fa9…\",\"account_uuid\":\"\",
//	                         \"session_id\":\"04c27413-…\"}"}
//
// The client builds it with a 512-character cap and drops fields to stay under
// it (`Ett=512` in the bundle), so session_id is not guaranteed present and its
// absence is not an error. A value that is not this object yields "": see the
// note above about bare user ids.
func SessionFromMetadata(body []byte) string {
	var envelope metadataEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	raw := envelope.Metadata.UserID
	// Cheaper than a failed parse, and it is the common case for every client
	// that sets user_id to a plain string.
	if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return ""
	}
	var ident struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(raw), &ident); err != nil {
		return ""
	}
	return CleanIdentifier(ident.SessionID)
}

// clientNames maps the product token a client puts at the front of its
// User-Agent to the name an operator would recognise.
//
// Measured from the same capture:
//
//	claude-cli/2.1.274 (external, sdk-cli)                     -> Claude Code
//	opencode/1.17.13 ai-sdk/provider-utils/4.0.27 runtime/bun  -> opencode
//	codex_cli_rs/0.54.0                                        -> Codex
//
// crush is absent because it would not route to the stub; it falls through to
// the default below, which prints whatever token it sends. That is the point of
// the default: an unknown client is named, not discarded, so a new one shows up
// in the UI the first time it connects instead of appearing as a blank.
var clientNames = map[string]string{
	"claude-cli":   "Claude Code",
	"opencode":     "opencode",
	"codex_cli_rs": "Codex",
	"crush":        "crush",
}

// ClientName is the product a User-Agent claims to be.
func ClientName(userAgent string) string {
	token, _, _ := strings.Cut(strings.TrimSpace(userAgent), "/")
	token, _, _ = strings.Cut(token, " ")
	if token == "" {
		return ""
	}
	if name, ok := clientNames[token]; ok {
		return name
	}
	return CleanIdentifier(token)
}
