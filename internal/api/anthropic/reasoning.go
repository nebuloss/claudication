package anthropic

import "strings"

// Carrying OpenAI reasoning through a Messages client.
//
// A ChatGPT reasoning model's thinking is an item of its answer, and with
// store:false the only copy is the one in the answer: encrypted_content,
// which the backend reads back when the item is sent again on the next turn
// (measured 2026-10-19, internal/provider/openai/testdata/replay.json, and
// 2026-10-20 here: replayed with and without its id, the second turn was
// answered from the first's reasoning).
//
// A Messages client has a place for exactly that: a thinking block, whose
// signature is opaque to the client and which a client that keeps thinking
// sends back with the turn (Claude Code does). So a reasoning item is
// answered as a thinking block whose text is the item's summary and whose
// signature carries the item, and a thinking block carrying one is sent back
// as the reasoning item it was.
//
// The two kinds of signature have to be told apart without doubt, because a
// conversation can hold both — a slot rebound from Claude to GPT mid-chat —
// and each upstream refuses the other's. Anthropic's signatures are base64
// (A-Z, a-z, 0-9, +, /, =), which never holds a colon; ours begin with
// signaturePrefix, which does. An Anthropic signature is never taken for
// ours, and ours, sent to Anthropic, is not one it could take for its own.

// signaturePrefix starts every signature this gateway writes. Versioned, so a
// later encoding can be told from this one.
const signaturePrefix = "claudication:openai-reasoning:v1:"

// carriedReasoning is a reasoning item as a signature holds it.
type carriedReasoning struct {
	ID        string
	Encrypted string
	// Summary is the reasoning's summary text, or "".
	Summary string
}

// item is the reasoning item as the backend takes it back.
func (r carriedReasoning) item() map[string]any {
	summary := []any{}
	if r.Summary != "" {
		summary = append(summary, map[string]any{"type": "summary_text", "text": r.Summary})
	}
	item := map[string]any{"type": "reasoning", "summary": summary, "encrypted_content": r.Encrypted}
	if r.ID != "" {
		item["id"] = r.ID
	}
	return item
}

// encodeSignature writes a reasoning item into a signature: the prefix, the
// item's id, a colon, its encrypted content. Neither holds a colon: the id is
// rs_ and hex, the content urlsafe base64 (a Fernet token, gAAAA…).
func encodeSignature(id, encrypted string) string {
	return signaturePrefix + id + ":" + encrypted
}

// decodeSignature reads one back. ok is false for any signature that is not
// ours, an Anthropic one above all.
func decodeSignature(sig string) (carriedReasoning, bool) {
	rest, ok := strings.CutPrefix(sig, signaturePrefix)
	if !ok {
		return carriedReasoning{}, false
	}
	id, enc, ok := strings.Cut(rest, ":")
	if !ok || enc == "" {
		return carriedReasoning{}, false
	}
	return carriedReasoning{ID: id, Encrypted: enc}, true
}
