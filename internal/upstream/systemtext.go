package upstream

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Phrasing out of Claude Code's own environment block.
//
// A system prompt that carries this line, but is not otherwise Claude Code's
// prompt, is refused with the same misleading message the MCP tool-name check
// produces — see mcpnames.go:
//
//	"Third-party apps now draw from your extra usage, not your plan limits."
//
// Measured against one captured opencode prompt (9,274 characters), one
// account, one minute:
//
//	full prompt verbatim                 400      the <env> block alone        200
//	env with the line removed            200      line in a user message       200
//	env with the line reworded           200      "Is dir a git repo:"         200
//	env without Working directory        400      env contents blanked         200
//	env without Platform / Today's date  400      <env> renamed <environment>  400
//
// So: it is that line, it counts only inside `system`, and the smallest
// rewording clears it. Note the line alone is not enough to be refused — the
// `<env>` block on its own passes — so this is not a string ban but a check
// that fires when Claude Code's phrasing turns up in a prompt that is not
// Claude Code's. Rewording is therefore the fix rather than deletion: the
// model keeps the fact, and the agent behaves the same.
const (
	claudeEnvLine    = "Is directory a git repo:"
	claudeEnvRewrite = "Git repository:"
)

// NormaliseSystem makes the system prompt something the upstream will accept
// from a client that is not Claude Code.
//
// Two things, both confined to `system` — the environment line is accepted
// outside it, measured, so rewriting it elsewhere would be changing a body for
// no reason. Empty blocks in `messages` are DropEmptyMessageText's job:
//
//   - The environment line above is reworded.
//   - Empty text blocks are dropped. The upstream refuses them outright
//     ("text content blocks must be non-empty") while accepting no `system`
//     key at all, so a client that sends one gets a hard failure for a block
//     that carries nothing.
//
// A body needing neither is returned as the caller's own bytes, for one scan
// of it. Like EnsureAttribution and RewriteMCPNames, this is a narrow, stated
// exception to relaying request bodies untouched: without it these requests do
// not work at all.
func NormaliseSystem(body []byte) []byte {
	if !mayNeedSystemNormalising(body) {
		return body
	}
	return normaliseSystem(body, json.Valid(body))
}

// mayNeedSystemNormalising is the cheap gate: one scan for each marker.
func mayNeedSystemNormalising(body []byte) bool {
	return bytes.Contains(body, []byte(claudeEnvLine)) || bytes.Contains(body, []byte(`"text":""`))
}

// normaliseSystem rewrites only the system value and splices it back.
//
// opencode puts the environment line in every request, so this ran on all of
// its traffic — and used to decode the whole envelope and re-encode the whole
// body, transcript included, to change one line of the system prompt. Now the
// system value alone is decoded and the body copied once around it. valid
// says the body is already known to be JSON, which the splice relies on; when
// it is not known, or the shape is one the splice will not touch, the old
// whole-envelope path decides.
func normaliseSystem(body []byte, valid bool) []byte {
	if !valid {
		return body // the slow path would refuse it too
	}
	_, start, end, found, ok := topLevelEntry(body, "system")
	if !ok {
		return normaliseSystemEnvelope(body)
	}
	if !found {
		return body
	}
	value, drop, changed := normaliseSystemValue(body[start:end])
	if !changed {
		return body
	}
	if drop {
		if out, ok := removeTopLevel(body, "system"); ok {
			return out
		}
		return normaliseSystemEnvelope(body)
	}
	return join(body[:start], value, body[end:])
}

// normaliseSystemValue rewrites a system value: the reworded line in a bare
// string or in any block, and empty blocks dropped. drop means every block was
// empty and the field should go.
func normaliseSystemValue(raw json.RawMessage) (out json.RawMessage, drop, changed bool) {
	// The Messages API takes either a bare string or a list of blocks. A
	// string cannot be an empty block, so it only wants the rewording.
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if !strings.Contains(text, claudeEnvLine) {
			return raw, false, false
		}
		encoded, err := json.Marshal(strings.ReplaceAll(text, claudeEnvLine, claudeEnvRewrite))
		if err != nil {
			return raw, false, false
		}
		return encoded, false, true
	}

	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return raw, false, false // neither shape; leave it for the upstream to reject
	}
	kept := make([]json.RawMessage, 0, len(blocks))
	for _, block := range blocks {
		out, dropBlock, ok := normaliseBlock(block)
		if !ok {
			kept = append(kept, block)
			continue
		}
		changed = true
		if !dropBlock {
			kept = append(kept, out)
		}
	}
	if !changed {
		return raw, false, false
	}
	// Every block was empty. Omitting `system` is accepted; an empty array is
	// not something the upstream promises anything about, so do not send one.
	if len(kept) == 0 {
		return nil, true, true
	}
	encoded, err := json.Marshal(kept)
	if err != nil {
		return raw, false, false
	}
	return encoded, false, true
}

// normaliseSystemEnvelope is the whole-envelope path, for a body whose shape
// the splice will not touch — a duplicated or escaped key.
func normaliseSystemEnvelope(body []byte) []byte {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return body
	}
	raw, ok := envelope["system"]
	if !ok {
		return body
	}
	value, drop, changed := normaliseSystemValue(raw)
	if !changed {
		return body
	}
	if drop {
		delete(envelope, "system")
	} else {
		envelope["system"] = value
	}
	return remarshal(body, envelope)
}

// normaliseBlock reports the rewritten block, whether it should be dropped for
// being empty, and whether anything changed at all.
func normaliseBlock(raw json.RawMessage) (out json.RawMessage, drop, changed bool) {
	var block map[string]json.RawMessage
	if err := json.Unmarshal(raw, &block); err != nil {
		return raw, false, false
	}
	var text string
	if err := json.Unmarshal(block["text"], &text); err != nil {
		return raw, false, false // no text of its own: an image, or a shape we do not know
	}
	if text == "" {
		return raw, true, true
	}
	if !strings.Contains(text, claudeEnvLine) {
		return raw, false, false
	}
	encoded, err := json.Marshal(strings.ReplaceAll(text, claudeEnvLine, claudeEnvRewrite))
	if err != nil {
		return raw, false, false
	}
	block["text"] = encoded
	rewritten, err := json.Marshal(block)
	if err != nil {
		return raw, false, false
	}
	return rewritten, false, true
}

// remarshal encodes the envelope, falling back to the original bytes rather
// than sending something half-built.
func remarshal(body []byte, envelope map[string]json.RawMessage) []byte {
	out, err := json.Marshal(envelope)
	if err != nil {
		return body
	}
	return out
}

// DropEmptyMessageText removes empty text blocks from message content.
//
// The upstream refuses them in `messages` exactly as it does in `system` —
// "messages: text content blocks must be non-empty" — and it refuses the whole
// request over a block that carries nothing. Measured: one empty block is a
// 400 even when a non-empty one sits beside it. An empty tool_result is
// accepted and is left alone.
//
// Separate from NormaliseSystem rather than folded into it: that one is shaped
// around the system array and its early exits, and the two only ever run
// together on a body carrying an empty block, which is rare enough that a
// second parse is cheaper than the restructuring.
//
// A message whose every block is empty keeps them. Emptying the array trades
// one refusal for another, and inventing filler text is not this gateway's
// decision to make.
func DropEmptyMessageText(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"text":""`)) {
		return body
	}
	return dropEmptyMessageText(body, json.Valid(body))
}

// dropEmptyMessageText rewrites only the messages that carry an empty block
// and copies everything else — the rest of the transcript included — once,
// as it is. It used to decode the envelope and the messages array and
// re-encode both, which is the whole body twice over for one empty block.
func dropEmptyMessageText(body []byte, valid bool) []byte {
	if !valid {
		return body
	}
	_, start, end, found, ok := topLevelEntry(body, "messages")
	if !ok {
		return dropEmptyMessageTextEnvelope(body)
	}
	if !found {
		return body
	}
	spans, ok := arrayElements(body, start, end)
	if !ok {
		return body // not an array; the upstream's to reject
	}

	var out []byte
	last := 0
	for _, sp := range spans {
		message := body[sp[0]:sp[1]]
		// Skip a message with nothing to drop without decoding it, which is
		// what keeps this off the critical path of a long transcript.
		if !bytes.Contains(message, []byte(`"text":""`)) {
			continue
		}
		rewritten, ok := dropEmptyBlocks(message)
		if !ok {
			continue
		}
		if out == nil {
			out = make([]byte, 0, len(body))
		}
		out = append(out, body[last:sp[0]]...)
		out = append(out, rewritten...)
		last = sp[1]
	}
	if out == nil {
		return body
	}
	return append(out, body[last:]...)
}

// dropEmptyBlocks rewrites one message without its empty text blocks. ok is
// false when there is nothing to drop, or dropping would leave no blocks.
func dropEmptyBlocks(message []byte) ([]byte, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(message, &fields); err != nil {
		return nil, false
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(fields["content"], &blocks); err != nil {
		return nil, false // a bare string carries no blocks
	}
	kept := make([]json.RawMessage, 0, len(blocks))
	for _, block := range blocks {
		if !emptyTextBlock(block) {
			kept = append(kept, block)
		}
	}
	if len(kept) == len(blocks) || len(kept) == 0 {
		return nil, false
	}
	encoded, err := json.Marshal(kept)
	if err != nil {
		return nil, false
	}
	fields["content"] = encoded
	rewritten, err := json.Marshal(fields)
	if err != nil {
		return nil, false
	}
	return rewritten, true
}

// dropEmptyMessageTextEnvelope is the whole-envelope path, for a body whose
// shape the splice will not touch.
func dropEmptyMessageTextEnvelope(body []byte) []byte {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return body
	}
	raw, ok := envelope["messages"]
	if !ok {
		return body
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		return body
	}
	changed := false
	for i, message := range messages {
		if !bytes.Contains(message, []byte(`"text":""`)) {
			continue
		}
		if rewritten, ok := dropEmptyBlocks(message); ok {
			messages[i] = rewritten
			changed = true
		}
	}
	if !changed {
		return body
	}
	encoded, err := json.Marshal(messages)
	if err != nil {
		return body
	}
	envelope["messages"] = encoded
	return remarshal(body, envelope)
}

// emptyTextBlock reports whether a content block is a text block carrying
// nothing. Anything else — an image, a tool_result, a shape we do not know —
// is not ours to drop.
func emptyTextBlock(raw json.RawMessage) bool {
	var block struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	}
	if err := json.Unmarshal(raw, &block); err != nil {
		return false
	}
	return block.Type == "text" && block.Text != nil && *block.Text == ""
}
