package upstream

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
}

// Peek reads the prologue. An unparseable body yields a zero Prologue and is
// left for the upstream to reject.
func Peek(body []byte) Prologue {
	var p Prologue
	_ = json.Unmarshal(body, &p)
	return p
}

// Claude Code's attribution block, and the gate it turns out to be.
//
// The subscription backend serves haiku to anything, but refuses opus and
// sonnet unless the first system block is one of these strings. The refusal
// arrives as 429 rate_limit_error with the message "Error", no rate-limit
// headers, and x-should-retry: true — so it reads as quota exhaustion and is
// nothing of the sort. Observed at 2% utilisation of the five-hour window.
//
// The strings are the client's own (bundle v2.1.263):
//
//	var F  = "You are Claude Code, Anthropic's official CLI for Claude.",
//	    Te = "…, running within the Claude Agent SDK.",
//	    Ee = "You are a Claude agent, built on Anthropic's Claude Agent SDK.",
//	    ft = [F, Te, Ee], PVt = new Set(ft)
//
// A Set, and position matters: the same string in the second slot is refused,
// so this is a first-block check rather than a search.
const ClaudeCodeAttribution = "You are Claude Code, Anthropic's official CLI for Claude."

var acceptedAttribution = map[string]bool{
	ClaudeCodeAttribution: true,
	"You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.": true,
	"You are a Claude agent, built on Anthropic's Claude Agent SDK.":                                 true,
}

// EnsureAttribution puts the attribution block first in the system array, so a
// client that is not Claude Code can still reach the models the subscription
// pays for.
//
// This is the one place the relay edits a request body, and it is a deliberate,
// switchable exception rather than an oversight — see passthrough.claude-code-
// attribution. Two things keep it as small as an exception can be:
//
//   - A body that already leads with an accepted block is returned untouched,
//     byte for byte. Claude Code's own traffic is never rewritten.
//   - Nothing else in the body is read or reordered beyond what re-encoding
//     the top-level object requires; the system array keeps its order and the
//     messages are copied across verbatim.
//
// A body that cannot be parsed is returned unchanged: it is the client's to
// get wrong, and the upstream's to reject.
//
// p is the prologue already read from body by Peek, so the common case — a
// body that is already attributed, which is all of Claude Code's traffic —
// costs no parsing at all here.
func EnsureAttribution(body []byte, p Prologue) []byte {
	blocks, ok := systemBlocks(p.System)
	if !ok {
		return body
	}
	if len(blocks) > 0 && acceptedAttribution[blockText(blocks[0])] {
		return body // already attributed; leave the bytes alone
	}

	// Only now, on the rare path that actually rewrites, is the whole envelope
	// parsed. Unmarshalling into map[string]json.RawMessage copies every
	// top-level value, so on a Claude Code turn — which resends the entire
	// transcript, routinely a megabyte or more — doing it up front duplicated
	// the body before deciding it had nothing to change.
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return body
	}

	attribution, err := json.Marshal(map[string]string{
		"type": "text",
		"text": ClaudeCodeAttribution,
	})
	if err != nil {
		return body
	}

	rewritten, err := json.Marshal(append([]json.RawMessage{attribution}, blocks...))
	if err != nil {
		return body
	}
	envelope["system"] = rewritten

	out, err := json.Marshal(envelope)
	if err != nil {
		return body
	}
	return out
}

// systemBlocks normalises the field into a block list. The Messages API
// accepts either a bare string or an array of blocks, and a client that sent a
// string still has a first block to displace.
func systemBlocks(raw json.RawMessage) ([]json.RawMessage, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, true
	}

	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err == nil {
		return blocks, true
	}

	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return nil, false // neither shape; leave it to the upstream
	}
	block, err := json.Marshal(map[string]string{"type": "text", "text": text})
	if err != nil {
		return nil, false
	}
	return []json.RawMessage{block}, true
}

func blockText(raw json.RawMessage) string {
	var block struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &block); err != nil {
		return ""
	}
	return block.Text
}
