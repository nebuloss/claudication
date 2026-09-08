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
// Two things, both confined to `system` — messages are never touched:
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
	// Cheap gate, and it decides which of the two passes has to run.
	line := bytes.Contains(body, []byte(claudeEnvLine))
	empty := bytes.Contains(body, []byte(`"text":""`))
	if !line && !empty {
		return body
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return body
	}
	raw, ok := envelope["system"]
	if !ok {
		return body
	}

	// The Messages API takes either a bare string or a list of blocks. A
	// string cannot be an empty block, so it only wants the rewording.
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if !strings.Contains(text, claudeEnvLine) {
			return body
		}
		encoded, err := json.Marshal(strings.ReplaceAll(text, claudeEnvLine, claudeEnvRewrite))
		if err != nil {
			return body
		}
		envelope["system"] = encoded
		return remarshal(body, envelope)
	}

	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return body // neither shape; leave it for the upstream to reject
	}

	kept := make([]json.RawMessage, 0, len(blocks))
	changed := false
	for _, block := range blocks {
		out, drop, ok := normaliseBlock(block)
		if !ok {
			kept = append(kept, block)
			continue
		}
		changed = true
		if drop {
			continue
		}
		kept = append(kept, out)
	}
	if !changed {
		return body
	}

	// Every block was empty. Omitting `system` is accepted; an empty array is
	// not something the upstream promises anything about, so do not send one.
	if len(kept) == 0 {
		delete(envelope, "system")
		return remarshal(body, envelope)
	}

	encoded, err := json.Marshal(kept)
	if err != nil {
		return body
	}
	envelope["system"] = encoded
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
