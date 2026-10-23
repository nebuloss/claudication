package anthropic

import (
	"bytes"
	"cmp"
	"encoding/json"
	"strings"

	"claudication/internal/anthropicwire"
	"claudication/internal/geminiwire"
)

// The answer half of Messages → Gemini: Code Assist's events, said as the
// Anthropic events a Messages client reads, in Anthropic's order.
//
// Each event of the stream is a whole envelope holding the parts produced
// since the last (internal/provider/google, meter.go), so there is no block
// structure to follow: a run of parts of one kind is one block.
//
//	first event                        message_start, ping
//	thought text (when asked for)      thinking block, thinking_delta
//	text                               text block, text_delta
//	functionCall                       tool_use block, its args at once
//	thoughtSignature on a call         a thinking block carrying it (when
//	                                     thinking was asked for), and kept
//	                                     by the gateway for the next turn
//	finishReason STOP                  end_turn, or tool_use after a call
//	finishReason MAX_TOKENS            max_tokens
//	SAFETY, RECITATION, …              refusal
//	promptFeedback.blockReason         refusal, with nothing said
//	MALFORMED_FUNCTION_CALL            error: the turn produced nothing usable
//	an error in place of an answer     error
//
// Not measured: every row is gemini-cli's reading of the same stream.

// fromGemini reads Code Assist.
var fromGemini = upstreamAnswer{
	convert: func(tr Translation, out emitter) answer { return newGeminiConverter(tr, out) },
	refusal: geminiError,
}

// geminiConverter is the state of one Gemini answer being translated.
type geminiConverter struct {
	tr  Translation
	out emitter

	started  bool
	finished bool
	next     int
	open     *openBlock
	// signed is that the open thinking block has its signature already.
	signed bool

	toolCalls bool
	usage     *geminiwire.UsageMetadata
	id        string
}

func newGeminiConverter(tr Translation, out emitter) *geminiConverter {
	tr.Body = nil
	return &geminiConverter{tr: tr, out: out}
}

func (c *geminiConverter) live() bool { return c.started && !c.finished }
func (c *geminiConverter) done() bool { return c.finished }

// geminiEvent is one streamed line: an answer, or an error in its place.
type geminiEvent struct {
	Response *geminiwire.GenerateResponse `json:"response"`
	TraceID  string                       `json:"traceId"`
	Error    *geminiwire.Status           `json:"error"`
}

func (c *geminiConverter) handle(data []byte) {
	if c.finished {
		return
	}
	var ev geminiEvent
	if json.Unmarshal(data, &ev) != nil {
		return
	}
	if ev.Error != nil {
		kind, msg := geminiStatusKind(ev.Error.Code, *ev.Error)
		c.fail(kind, msg)
		return
	}
	r := ev.Response
	if r == nil {
		return
	}
	c.start(cmp.Or(r.ResponseID, ev.TraceID))
	if r.UsageMetadata != nil {
		c.usage = r.UsageMetadata
	}
	if f := r.PromptFeedback; f != nil && f.BlockReason != "" && f.BlockReason != "BLOCK_REASON_UNSPECIFIED" {
		c.finish("refusal")
		return
	}
	if len(r.Candidates) == 0 {
		return
	}
	cand := r.Candidates[0]
	if cand.Content != nil {
		for _, p := range cand.Content.Parts {
			c.part(p)
		}
	}
	switch cand.FinishReason {
	case "", "FINISH_REASON_UNSPECIFIED":
	case geminiwire.FinishStop:
		if c.toolCalls {
			c.finish("tool_use")
		} else {
			c.finish("end_turn")
		}
	case geminiwire.FinishMaxTokens:
		c.finish("max_tokens")
	case geminiwire.FinishMalformedFunctionCall:
		c.fail("api_error", "Gemini produced a malformed tool call")
	default:
		c.finish("refusal")
	}
}

// start sends message_start, once.
func (c *geminiConverter) start(id string) {
	if c.started {
		return
	}
	c.started = true
	c.id = "msg_gateway"
	if id != "" {
		c.id = "msg_" + id
	}
	c.out.emit(anthropicwire.EventMessageStart, map[string]any{
		"type": anthropicwire.EventMessageStart,
		"message": messageStart{
			ID: c.id, Type: "message", Role: "assistant", Model: c.tr.Model, Content: []any{},
		},
	})
	c.out.emit(anthropicwire.EventPing, map[string]any{"type": anthropicwire.EventPing})
}

// part says one part: into the block of its kind, opened when the last part
// was of another kind.
func (c *geminiConverter) part(p geminiwire.Part) {
	switch {
	case p.FunctionCall != nil:
		c.call(p)
	case p.Thought:
		if !c.tr.Thinking {
			return
		}
		b := c.openKind("thinking", map[string]any{"type": "thinking", "thinking": "", "signature": ""})
		if p.Text != "" && !c.tr.Omitted {
			c.delta(b, map[string]any{"type": "thinking_delta", "thinking": p.Text})
		}
		if p.ThoughtSignature != "" {
			c.sign(b, p.ThoughtSignature)
		}
	case p.Text != "":
		b := c.openKind("text", map[string]any{"type": "text", "text": ""})
		c.delta(b, map[string]any{"type": "text_delta", "text": p.Text})
	}
}

// call says a function call as a tool_use block, after the thinking block
// that carries its signature when the caller keeps thinking. The gateway
// keeps the signature against the call's id either way: a caller that asked
// for thinking may still not send it back — a Responses client reached
// through Messages has nowhere to keep a signature at all.
func (c *geminiConverter) call(p geminiwire.Part) {
	id := p.FunctionCall.ID
	if !validToolID(id) {
		id = newCallID()
	}
	if sig := p.ThoughtSignature; sig != "" {
		c.tr.reasoning.put(c.tr.conversation, callKey(id), []carriedReasoning{{Encrypted: sig}})
		if c.tr.Thinking {
			// The open thinking block, unless it is signed already: then the
			// call's own signature needs a block of its own.
			b := c.open
			if b == nil || b.kind != "thinking" || c.signed {
				b = c.begin("thinking", map[string]any{"type": "thinking", "thinking": "", "signature": ""})
			}
			c.sign(b, sig)
		}
	}
	c.closeOpen()
	args := "{}"
	if len(p.FunctionCall.Args) > 0 && string(p.FunctionCall.Args) != "null" {
		var compact bytes.Buffer
		if json.Compact(&compact, p.FunctionCall.Args) == nil {
			args = compact.String()
		}
	}
	b := c.begin("tool_use", map[string]any{
		"type": "tool_use", "id": id, "name": c.toolName(p.FunctionCall.Name), "input": map[string]any{},
	})
	c.delta(b, map[string]any{"type": "input_json_delta", "partial_json": args})
	c.closeOpen()
	c.toolCalls = true
}

// validToolID is an id a Messages client and Anthropic both take as a
// tool_use id: ^[a-zA-Z0-9_-]+$.
func validToolID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func (c *geminiConverter) toolName(up string) string {
	if own, ok := c.tr.Tools[up]; ok {
		return own
	}
	return up
}

// openKind is the open block when it is of kind, else a new one; kind ""
// only closes what is open.
func (c *geminiConverter) openKind(kind string, block map[string]any) *openBlock {
	if c.open != nil && c.open.kind == kind {
		return c.open
	}
	c.closeOpen()
	if kind == "" {
		return nil
	}
	return c.begin(kind, block)
}

func (c *geminiConverter) begin(kind string, block map[string]any) *openBlock {
	c.closeOpen()
	b := &openBlock{index: c.next, kind: kind}
	c.next++
	c.open, c.signed = b, false
	c.out.emit(anthropicwire.EventContentBlockStart, map[string]any{
		"type": anthropicwire.EventContentBlockStart, "index": b.index, "content_block": block,
	})
	return b
}

func (c *geminiConverter) delta(b *openBlock, d map[string]any) {
	if b == nil {
		return
	}
	b.streamed = true
	c.out.emit(anthropicwire.EventContentBlockDelta, map[string]any{
		"type": anthropicwire.EventContentBlockDelta, "index": b.index, "delta": d,
	})
}

func (c *geminiConverter) sign(b *openBlock, sig string) {
	c.delta(b, map[string]any{"type": "signature_delta", "signature": encodeGeminiSignature(sig)})
	c.signed = true
}

func (c *geminiConverter) closeOpen() {
	if c.open == nil {
		return
	}
	c.out.emit(anthropicwire.EventContentBlockStop, map[string]any{
		"type": anthropicwire.EventContentBlockStop, "index": c.open.index,
	})
	c.open = nil
}

// finish ends the turn: message_delta with the stop reason and the usage, as
// the Google meter normalises it, then message_stop.
func (c *geminiConverter) finish(reason string) {
	if c.finished {
		return
	}
	c.start("")
	c.closeOpen()
	usage := map[string]any{}
	if u := c.usage; u != nil {
		cached := u.CachedContentTokenCount
		usage["input_tokens"] = max(u.PromptTokenCount+u.ToolUsePromptTokenCount-cached, 0)
		usage["cache_read_input_tokens"] = cached
		usage["cache_creation_input_tokens"] = 0
		usage["output_tokens"] = u.CandidatesTokenCount + u.ThoughtsTokenCount
	}
	c.out.emit(anthropicwire.EventMessageDelta, map[string]any{
		"type":  anthropicwire.EventMessageDelta,
		"delta": map[string]any{"stop_reason": reason, "stop_sequence": nil},
		"usage": usage,
	})
	c.out.emit(anthropicwire.EventMessageStop, map[string]any{"type": anthropicwire.EventMessageStop})
	c.finished = true
}

// fail ends the turn with Anthropic's error event.
func (c *geminiConverter) fail(kind, message string) {
	if c.finished {
		return
	}
	c.closeOpen()
	c.out.emit(anthropicwire.EventError, map[string]any{
		"type":  anthropicwire.EventError,
		"error": map[string]string{"type": kind, "message": message},
	})
	c.finished = true
}

// whole reads an answer that came as one body: one envelope, or an array of
// them, which is what the streamed method answers without ?alt=sse.
func (c *geminiConverter) whole(body []byte) (func(), error) {
	trimmed := bytes.TrimSpace(body)
	var events []json.RawMessage
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &events); err != nil {
			return nil, err
		}
	} else {
		var probe geminiEvent
		if err := json.Unmarshal(trimmed, &probe); err != nil {
			return nil, err
		}
		events = []json.RawMessage{trimmed}
	}
	return func() {
		for _, ev := range events {
			c.handle(ev)
		}
		if !c.finished {
			if c.toolCalls {
				c.finish("tool_use")
			} else {
				c.finish("end_turn")
			}
		}
	}, nil
}

// geminiError reads a Code Assist refusal as Anthropic's error type and the
// backend's own words.
func geminiError(status int, body []byte) (kind, message string) {
	var e geminiwire.ErrorEnvelope
	if json.Unmarshal(body, &e) != nil || (e.Error.Message == "" && e.Error.Status == "") {
		var arr []geminiwire.ErrorEnvelope
		if json.Unmarshal(body, &arr) == nil && len(arr) > 0 {
			e = arr[0]
		}
	}
	if e.Error.Message != "" || e.Error.Status != "" {
		return geminiStatusKind(status, e.Error)
	}
	if s := strings.TrimSpace(string(body)); s != "" {
		if len(s) > 2000 {
			s = s[:2000]
		}
		return errorKind(status, "", ""), s
	}
	return errorKind(status, "", ""), "Gemini refused the request without saying why"
}

// geminiStatusKind is the Anthropic error type for a google.rpc status: by
// its canonical name, else by the HTTP status. A Messages client retries a
// rate_limit_error and an overloaded_error after a wait and stops at the
// rest, so which is which decides whether it retries.
func geminiStatusKind(status int, s geminiwire.Status) (string, string) {
	msg := cmp.Or(s.Message, s.Status, "Gemini refused the request")
	switch s.Status {
	case "RESOURCE_EXHAUSTED":
		return "rate_limit_error", msg
	case "UNAVAILABLE":
		return "overloaded_error", msg
	case "INVALID_ARGUMENT", "FAILED_PRECONDITION", "OUT_OF_RANGE":
		return "invalid_request_error", msg
	case "PERMISSION_DENIED":
		return "permission_error", msg
	case "UNAUTHENTICATED":
		return "authentication_error", msg
	case "NOT_FOUND":
		return "not_found_error", msg
	}
	if status == 0 {
		status = s.Code
	}
	return errorKind(status, "", ""), msg
}
