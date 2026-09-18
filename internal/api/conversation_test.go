package api

import (
	"net/http"
	"strings"
	"testing"
)

// The bodies and header values here are real, captured from each client on
// 2026-09-17 by pointing it at a stub and reading the wire. Keeping the real
// shapes means a client changing its mind breaks a test rather than quietly
// producing ungrouped rows.

func TestSessionFromMetadata(t *testing.T) {
	// Exactly what Claude Code 2.1.274 sends: a JSON *string* holding JSON.
	const real = `{"model":"claude-opus-5","metadata":{"user_id":` +
		`"{\"device_id\":\"0fa99d6fa5b10a6928ef0ba2db6981132e3e9a55987dd4800c9050ccba34e59a\"` +
		`,\"account_uuid\":\"\",\"session_id\":\"04c27413-cc40-4b20-be7c-e33c5b61ff3d\"}"}}`

	if got := SessionFromMetadata([]byte(real)); got != "04c27413-cc40-4b20-be7c-e33c5b61ff3d" {
		t.Errorf("session_id = %q, want the uuid out of the nested JSON", got)
	}

	// A bare user id names a person, not a chat. Taking it would collapse
	// every conversation they ever have into a single row.
	const bare = `{"metadata":{"user_id":"user_abc123"}}`
	if got := SessionFromMetadata([]byte(bare)); got != "" {
		t.Errorf("bare user_id = %q, want empty: it identifies a user, not a chat", got)
	}

	for _, body := range []string{
		`{"metadata":{}}`,
		`{}`,
		`not json at all`,
		``,
		// The client caps the blob at 512 chars and drops fields to fit, so a
		// well-formed object with no session_id is expected, not an error.
		`{"metadata":{"user_id":"{\"device_id\":\"d\"}"}}`,
	} {
		if got := SessionFromMetadata([]byte(body)); got != "" {
			t.Errorf("SessionFromMetadata(%.30q) = %q, want empty", body, got)
		}
	}
}

func TestHeaderIDPrefersTheMoreSpecificHeader(t *testing.T) {
	h := http.Header{}
	h.Set("X-Session-Id", "ses_generic")
	h.Set("X-Claude-Code-Session-Id", "04c27413-cc40-4b20-be7c-e33c5b61ff3d")

	got := HeaderID(h, "X-Claude-Code-Session-Id", "X-Session-Id")
	if got != "04c27413-cc40-4b20-be7c-e33c5b61ff3d" {
		t.Errorf("= %q, want the first name listed to win", got)
	}
	if got := HeaderID(http.Header{}, "X-Session-Id"); got != "" {
		t.Errorf("no header at all = %q, want empty", got)
	}
}

func TestCleanIdentifierBoundsWhatAClientCanStore(t *testing.T) {
	// Control characters are stripped because this value is printed in a log
	// line and in the admin UI; an escape sequence in an id rewrites what an
	// operator sees.
	if got := CleanIdentifier("ses_\x1b[31mred\x00"); got != "ses_[31mred" {
		t.Errorf("= %q, want the escape and the NUL gone", got)
	}
	if got := CleanIdentifier("  ses_padded  "); got != "ses_padded" {
		t.Errorf("= %q, want it trimmed", got)
	}
	long := CleanIdentifier(strings.Repeat("x", 4000))
	if len(long) != maxIdentifier {
		t.Errorf("length = %d, want it capped at %d: a column that accepts a "+
			"megabyte is a way to fill a disk one request at a time",
			len(long), maxIdentifier)
	}
	if got := CleanIdentifier("\x00\x01"); got != "" {
		t.Errorf("= %q, want empty rather than a blank-looking id", got)
	}
}

func TestClientName(t *testing.T) {
	for _, c := range []struct{ ua, want string }{
		{"claude-cli/2.1.274 (external, sdk-cli)", "Claude Code"},
		{"opencode/1.17.13 ai-sdk/provider-utils/4.0.27 runtime/bun/1.3.14", "opencode"},
		{"codex_cli_rs/0.54.0", "Codex"},
		// Read off production traffic, not a stub: crush calls itself
		// Charm-Crush, which is not what this map first guessed.
		{"Charm-Crush/0.93.1", "crush"},
		// An unknown client is named from its own token rather than discarded,
		// so a new one is visible the first time it connects.
		{"something-new/9.9", "something-new"},
		{"", ""},
	} {
		if got := ClientName(c.ua); got != c.want {
			t.Errorf("ClientName(%q) = %q, want %q", c.ua, got, c.want)
		}
	}
}
