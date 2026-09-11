package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"claudication/internal/api"
)

// now is a variable so tests can pin created_at.
var now = time.Now

// Events Codex acts on. Everything else it debug-logs and ignores, so the few
// we send outside this list (in_progress) are keepalives by design, not noise.
const (
	evCreated     = "response.created"
	evInProgress  = "response.in_progress"
	evItemAdded   = "response.output_item.added"
	evItemDone    = "response.output_item.done"
	evTextDelta   = "response.output_text.delta"
	evReasonDelta = "response.reasoning_text.delta"
	evCompleted   = "response.completed"
	evFailed      = "response.failed"
	evIncomplete  = "response.incomplete"
)

// Stream converts one Anthropic SSE response into one Responses SSE response.
//
// It is a state machine over Anthropic's block events, because the two
// protocols disagree about what a "block" is: Anthropic opens, streams and
// closes numbered content blocks, while Responses emits whole items with
// deltas attached to them. The mapping is per-block and the bookkeeping below
// is what makes it one-to-one.
type Stream struct {
	w      api.FlushWriter
	req    Request
	seq    int
	respID string
	model  string

	// The open Anthropic content block, if any.
	open     bool
	kind     string // text | thinking | tool_use
	itemID   string
	outIndex int
	text     strings.Builder
	toolName string
	toolID   string
	toolArgs strings.Builder

	// pending holds bytes that do not yet make a whole SSE frame.
	pending bytes.Buffer

	// Items as they finish, for the output array on the final event.
	items []json.RawMessage

	usage      responsesUsage
	stopReason string
	// finished records that a terminal event has gone out, so a later error
	// cannot send a second one. Codex reads the first and a second would be a
	// frame arriving after the turn it belongs to.
	finished bool
}

type responsesUsage struct {
	InputTokens         int           `json:"input_tokens"`
	InputTokensDetails  inputDetails  `json:"input_tokens_details"`
	OutputTokens        int           `json:"output_tokens"`
	OutputTokensDetails outputDetails `json:"output_tokens_details"`
	TotalTokens         int           `json:"total_tokens"`
}

type inputDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type outputDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

type responsesError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// NewStream prepares a translator writing Responses frames to w. It satisfies
// api.StreamWriter, which is what the reshaping sink drives it through.
func NewStream(w api.FlushWriter, req Request) *Stream {
	return &Stream{w: w, req: req, model: req.Model, items: []json.RawMessage{}}
}

// Write takes Anthropic SSE bytes as they arrive and emits the Responses
// frames they imply.
//
// Push rather than pull, because the relay hands its bytes to an
// http.ResponseWriter and pulling would mean a pipe and a second goroutine
// between two things that must stay in lockstep: every Anthropic chunk has to
// become a flushed Responses frame before the next one arrives, or the idle
// gap Codex fails a turn on opens up inside our own plumbing.
func (s *Stream) Write(p []byte) (int, error) {
	s.pending.Write(p)
	for {
		raw, ok := takeFrame(&s.pending)
		if !ok {
			return len(p), nil
		}
		if err := s.frame(raw); err != nil {
			return len(p), err
		}
	}
}

// Finish ends the turn, whatever state the source left it in. cause is the
// error that stopped it, or nil for a clean end.
//
// It always leaves a terminal event behind. A source that stops early — a
// dropped connection, a context cancelled — still gets a response.failed,
// because the alternative is Codex reporting "stream closed before
// response.completed", which says nothing about what actually happened.
func (s *Stream) Finish(cause error) {
	if rest := bytes.TrimSpace(s.pending.Bytes()); len(rest) > 0 {
		// A last frame with no blank line after it. Anthropic terminates
		// properly, but a connection cut between the JSON and the newlines
		// would otherwise throw away a message_stop we did receive.
		_ = s.frame(rest)
	}
	s.pending.Reset()
	if s.finished {
		return
	}
	msg := "the upstream ended the stream before it completed"
	if cause != nil {
		msg = cause.Error()
	}
	_ = s.Fail("server_error", msg)
}

// Run drives the whole stream from a reader. Finish is called for you.
func (s *Stream) Run(src io.Reader) error {
	_, err := io.Copy(s, src)
	s.Finish(err)
	return err
}

// takeFrame removes the next complete SSE frame from buf, if there is one.
// Frames end at a blank line, in either line ending.
func takeFrame(buf *bytes.Buffer) ([]byte, bool) {
	b := buf.Bytes()
	lf := bytes.Index(b, []byte("\n\n"))
	crlf := bytes.Index(b, []byte("\r\n\r\n"))
	end, sep := lf, 2
	if crlf >= 0 && (end < 0 || crlf < end) {
		end, sep = crlf, 4
	}
	if end < 0 {
		return nil, false
	}
	frame := make([]byte, end)
	copy(frame, b[:end])
	buf.Next(end + sep)
	return frame, true
}

// frame parses one SSE frame and dispatches it.
func (s *Stream) frame(raw []byte) error {
	var event, data string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			// Multi-line data fields concatenate with a newline. Anthropic
			// does not use them, but the format allows them and a parser that
			// dropped the second line would silently truncate the JSON.
			part := strings.TrimPrefix(line[len("data:"):], " ")
			if data == "" {
				data = part
			} else {
				data += "\n" + part
			}
		default:
			// A comment (": ping") or a field we do not use.
		}
	}
	if data == "" && event == "" {
		return nil
	}
	return s.handle(event, []byte(data))
}

// anthropicEvent is the union of the streaming events, read loosely: fields
// absent from a given event stay zero.
type anthropicEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			CacheReadInput     int `json:"cache_read_input_tokens"`
			CacheCreationInput int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	ContentBlock struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		Thinking    string `json:"thinking"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage struct {
		OutputTokens int `json:"output_tokens"`
		InputTokens  int `json:"input_tokens"`
	} `json:"usage"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (s *Stream) handle(event string, data []byte) error {
	var e anthropicEvent
	if err := json.Unmarshal(data, &e); err != nil {
		// Not fatal on its own: the event name still tells us what it was, and
		// a ping we cannot parse is still a ping. Only a frame we can neither
		// name nor parse is worth giving up over.
		if event == "" {
			return nil
		}
		e.Type = event
	}
	if e.Type == "" {
		e.Type = event
	}

	switch e.Type {
	case "message_start":
		s.respID = "resp_" + strings.TrimPrefix(e.Message.ID, "msg_")
		if e.Message.Model != "" {
			s.model = e.Message.Model
		}
		u := e.Message.Usage
		s.usage.InputTokens = u.InputTokens + u.CacheReadInput + u.CacheCreationInput
		s.usage.InputTokensDetails.CachedTokens = u.CacheReadInput
		s.usage.OutputTokens = u.OutputTokens
		return s.emit(evCreated, map[string]any{
			"type":     evCreated,
			"response": s.response("in_progress", nil, nil),
		})

	case "ping":
		// Anthropic's keepalive during a long think. It has to become a frame
		// Codex reads and discards, not a raw comment: an idle gap fails the
		// turn, and in_progress is on Codex's explicit ignore list.
		return s.emit(evInProgress, map[string]any{
			"type":     evInProgress,
			"response": s.response("in_progress", nil, nil),
		})

	case "content_block_start":
		return s.startBlock(e)

	case "content_block_delta":
		return s.deltaBlock(e)

	case "content_block_stop":
		return s.stopBlock()

	case "message_delta":
		if e.Delta.StopReason != "" {
			s.stopReason = e.Delta.StopReason
		}
		if e.Usage.OutputTokens > 0 {
			s.usage.OutputTokens = e.Usage.OutputTokens
		}
		if e.Usage.InputTokens > 0 {
			s.usage.InputTokens = e.Usage.InputTokens
		}
		return nil

	case "message_stop":
		return s.complete()

	case "error":
		// A mid-stream upstream error. Closing here would tell Codex only that
		// the stream ended; a response.failed carries what went wrong.
		code, msg := mapError(e.Error.Type, e.Error.Message)
		return s.Fail(code, msg)
	}
	return nil
}

func (s *Stream) startBlock(e anthropicEvent) error {
	// A block opening while one is open means we missed a stop. Close it
	// rather than interleave, which would attach deltas to the wrong item.
	if s.open {
		if err := s.stopBlock(); err != nil {
			return err
		}
	}
	s.open = true
	s.kind = e.ContentBlock.Type
	s.text.Reset()
	s.toolArgs.Reset()
	s.outIndex = len(s.items)

	switch s.kind {
	case "tool_use":
		s.toolName = e.ContentBlock.Name
		s.toolID = e.ContentBlock.ID
		s.itemID = "fc_" + strings.TrimPrefix(s.toolID, "toolu_")
		return s.emit(evItemAdded, map[string]any{
			"type":         evItemAdded,
			"output_index": s.outIndex,
			"item":         s.functionCallItem("in_progress", "{}"),
		})

	case "thinking", "redacted_thinking":
		s.itemID = fmt.Sprintf("rs_%s_%d", s.respID, e.Index)
		return s.emit(evItemAdded, map[string]any{
			"type":         evItemAdded,
			"output_index": s.outIndex,
			"item":         s.reasoningItem("in_progress", ""),
		})

	default:
		s.kind = "text"
		s.text.WriteString(e.ContentBlock.Text)
		s.itemID = fmt.Sprintf("msg_%s_%d", s.respID, e.Index)
		return s.emit(evItemAdded, map[string]any{
			"type":         evItemAdded,
			"output_index": s.outIndex,
			"item":         s.messageItem("in_progress", ""),
		})
	}
}

func (s *Stream) deltaBlock(e anthropicEvent) error {
	if !s.open {
		return nil
	}
	switch e.Delta.Type {
	case "text_delta":
		s.text.WriteString(e.Delta.Text)
		return s.emit(evTextDelta, map[string]any{
			"type":          evTextDelta,
			"item_id":       s.itemID,
			"output_index":  s.outIndex,
			"content_index": 0,
			"delta":         e.Delta.Text,
		})

	case "thinking_delta":
		s.text.WriteString(e.Delta.Thinking)
		return s.emit(evReasonDelta, map[string]any{
			"type":          evReasonDelta,
			"item_id":       s.itemID,
			"output_index":  s.outIndex,
			"content_index": 0,
			"delta":         e.Delta.Thinking,
		})

	case "input_json_delta":
		// Buffered, not forwarded: Codex ignores the incremental
		// function_call_arguments events and takes the call whole from
		// output_item.done.
		s.toolArgs.WriteString(e.Delta.PartialJSON)
		return nil

	case "signature_delta":
		// Anthropic's thinking signature has no counterpart and is not ours to
		// invent one for.
		return nil
	}
	return nil
}

func (s *Stream) stopBlock() error {
	if !s.open {
		return nil
	}
	s.open = false

	var item map[string]any
	switch s.kind {
	case "tool_use":
		args := s.toolArgs.String()
		if strings.TrimSpace(args) == "" {
			// A tool called with no arguments streams no input_json_delta at
			// all. "{}" is the empty object; "" is not valid JSON and Codex
			// would fail to parse the call.
			args = "{}"
		}
		item = s.functionCallItem("completed", args)
	case "thinking", "redacted_thinking":
		item = s.reasoningItem("completed", s.text.String())
	default:
		item = s.messageItem("completed", s.text.String())
	}

	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	s.items = append(s.items, raw)
	return s.emit(evItemDone, map[string]any{
		"type":         evItemDone,
		"output_index": s.outIndex,
		"item":         item,
	})
}

func (s *Stream) messageItem(status, text string) map[string]any {
	content := []map[string]any{}
	if text != "" || status == "completed" {
		content = append(content, map[string]any{
			"type":        "output_text",
			"text":        text,
			"annotations": []any{},
		})
	}
	return map[string]any{
		"type":    "message",
		"id":      s.itemID,
		"status":  status,
		"role":    "assistant",
		"content": content,
	}
}

func (s *Stream) reasoningItem(status, text string) map[string]any {
	content := []map[string]any{}
	if text != "" {
		content = append(content, map[string]any{"type": "reasoning_text", "text": text})
	}
	return map[string]any{
		"type":    "reasoning",
		"id":      s.itemID,
		"status":  status,
		"summary": []any{},
		"content": content,
	}
}

func (s *Stream) functionCallItem(status, args string) map[string]any {
	name, namespace := s.toolName, ""
	if origin, ok := s.req.Tools[s.toolName]; ok {
		if origin.Name != "" {
			name = origin.Name
		}
		namespace = origin.Namespace
	}
	item := map[string]any{
		"type":   "function_call",
		"id":     s.itemID,
		"status": status,
		"name":   name,
		// call_id is what comes back on the next turn's
		// function_call_output, and convertInput maps that straight onto
		// tool_use_id. Using Anthropic's own id makes the round trip exact.
		"call_id":   s.toolID,
		"arguments": args,
	}
	if namespace != "" {
		item["namespace"] = namespace
	}
	return item
}

func (s *Stream) complete() error {
	if s.finished {
		return nil
	}
	if err := s.stopBlock(); err != nil {
		return err
	}
	s.finished = true
	s.usage.TotalTokens = s.usage.InputTokens + s.usage.OutputTokens

	// Truncation is what response.incomplete means, and saying so is better
	// than a completed turn that quietly stops mid-sentence.
	if s.stopReason == "max_tokens" {
		return s.emit(evIncomplete, map[string]any{
			"type": evIncomplete,
			"response": s.responseWith("incomplete", &s.usage, nil, map[string]any{
				"reason": "max_output_tokens",
			}),
		})
	}
	return s.emit(evCompleted, map[string]any{
		"type":     evCompleted,
		"response": s.response("completed", &s.usage, nil),
	})
}

// Fail ends the turn with an error Codex can read. It is also the entry point
// for a failure before any Anthropic byte arrived — an upstream that answered
// 429 to the request itself, say — because Codex has no way to learn about a
// turn that never produced a frame.
func (s *Stream) Fail(code, message string) error {
	if s.finished {
		return nil
	}
	s.finished = true
	if s.respID == "" {
		s.respID = fmt.Sprintf("resp_%d", now().UnixNano())
	}
	return s.emit(evFailed, map[string]any{
		"type": evFailed,
		"response": s.response("failed", nil, &responsesError{
			Code:    code,
			Message: message,
		}),
	})
}

func (s *Stream) response(status string, usage *responsesUsage, errObj *responsesError) map[string]any {
	return s.responseWith(status, usage, errObj, nil)
}

func (s *Stream) responseWith(status string, usage *responsesUsage, errObj *responsesError, incomplete map[string]any) map[string]any {
	id := s.respID
	if id == "" {
		// Codex requires an id on response.completed and treats its absence as
		// a parse failure of the whole turn.
		id = fmt.Sprintf("resp_%d", now().UnixNano())
	}
	out := map[string]any{
		"id":         id,
		"object":     "response",
		"created_at": now().Unix(),
		"status":     status,
		"model":      s.model,
		"output":     s.items,
	}
	if usage != nil {
		out["usage"] = usage
	}
	if errObj != nil {
		out["error"] = errObj
	}
	if incomplete != nil {
		out["incomplete_details"] = incomplete
	}
	return out
}

func (s *Stream) emit(event string, payload map[string]any) error {
	s.seq++
	payload["sequence_number"] = s.seq
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	// Every frame, not every batch: an idle gap fails the turn, so a frame
	// sitting in a buffer is indistinguishable from a gateway that hung.
	s.w.Flush()
	return nil
}

// mapError turns an Anthropic error into one of the codes Codex recognises.
//
// The list it acts on is context_length_exceeded, insufficient_quota,
// usage_not_included, invalid_prompt, cyber_policy, server_is_overloaded and
// slow_down. rate_limit_exceeded is NOT on it, which is why a rate limit maps
// to slow_down: that is the code that makes Codex back off, and the obvious
// name is the one that falls through to a generic retry.
//
// A kind that is not one of Anthropic's own is passed through rather than
// flattened. Those come from the gateway itself — "not_found" for a surface
// that is switched off, "no_accounts" for one with nothing connected — and
// neither is a server error. Calling them one would tell a client to retry
// something that will not change until a person changes it.
func mapError(kind, message string) (string, string) {
	if message == "" {
		message = "upstream error"
	}
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "prompt is too long"),
		strings.Contains(lower, "context window"),
		strings.Contains(lower, "too many tokens"):
		return "context_length_exceeded", message
	}
	switch kind {
	case "overloaded_error", "api_error":
		return "server_is_overloaded", message
	case "rate_limit_error":
		return "slow_down", message
	case "authentication_error", "permission_error":
		return "usage_not_included", message
	case "invalid_request_error":
		return "invalid_prompt", message
	case "":
		// An upstream body we could not read the type out of.
		return "server_error", message
	}
	return kind, message
}
