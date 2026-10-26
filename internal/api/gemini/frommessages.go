package gemini

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"claudication/internal/anthropicwire"
	"claudication/internal/api"
	"claudication/internal/geminiwire"
)

// The answer half of Gemini → Messages: Claude's answer said as the Gemini
// API's, which @google/genai reads.
//
//	Messages                          Gemini
//	text_delta                        a chunk with a text part
//	thinking_delta                    a thought part, when includeThoughts
//	a thinking block, ended           its text and signature carried in the
//	                                    thoughtSignature of the next part
//	tool_use, ended                   a functionCall part, args at once
//	message_delta end_turn, tool_use,
//	  stop_sequence                   finishReason STOP
//	message_delta max_tokens          MAX_TOKENS
//	message_delta refusal             SAFETY
//	usage                             usageMetadata: prompt = input + cache
//	                                    read + cache write, cached =
//	                                    cache read
//	error event                       a chunk {"error": {...}}, which the
//	                                    SDK throws
//
// A stream is `data:` lines of whole GenerateContentResponses with nothing
// between them but blank lines, as the Gemini API sends it with ?alt=sse.

// messagesExchange is one Gemini request in flight to Claude, or through
// Messages to another upstream.
type messagesExchange struct{ tr translation }

func (e *messagesExchange) Request() []byte {
	body := e.tr.body
	e.tr.body = nil
	return body
}
func (e *messagesExchange) Model() string   { return e.tr.model }
func (e *messagesExchange) Streaming() bool { return e.tr.stream }

// Headers drops the ones that describe the Gemini API: the key header, which
// authenticated the caller to us, and the installation id @google/genai's
// callers send. The User-Agent stays: it says truly what made the request.
func (e *messagesExchange) Headers(h http.Header) { dropDialectHeaders(h) }

func dropDialectHeaders(h http.Header) {
	h.Del("X-Goog-Api-Key")
	h.Del("X-Goog-Api-Client")
	h.Del("X-Gemini-Api-Privileged-User-Id")
	h.Set("Content-Type", "application/json")
}

func (e *messagesExchange) Sink(w http.ResponseWriter) api.Sink {
	return api.NewReshapingSink(w, e)
}

func (e *messagesExchange) Stream(out api.FlushWriter) api.StreamWriter {
	return newGeminiStream(out, e.tr)
}

func (e *messagesExchange) Complete(body []byte) ([]byte, error) {
	var m struct {
		ID         string                       `json:"id"`
		Model      string                       `json:"model"`
		StopReason string                       `json:"stop_reason"`
		Content    []anthropicwire.ContentBlock `json:"content"`
		Usage      anthropicwire.Usage          `json:"usage"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	s := &geminiStream{tr: e.tr}
	var parts []geminiwire.Part
	for _, b := range m.Content {
		parts = append(parts, s.blockParts(b)...)
	}
	if s.signature != "" {
		parts = append(parts, geminiwire.Part{Text: "", ThoughtSignature: s.signature})
	}
	r := s.response(parts, finishReason(m.StopReason), usageOf(m.Usage, m.Usage))
	r.ResponseID = strings.TrimPrefix(m.ID, "msg_")
	return json.Marshal(r)
}

// Refusal says Claude's error in Google's envelope, with the status the
// answer came with.
func (e *messagesExchange) Refusal(status int, body []byte) []byte {
	var env anthropicwire.ErrorEnvelope
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &env) == nil && env.Error.Message != "" {
		msg = env.Error.Message
	}
	return errorBody(status, statusName(status), msg)
}

func (e *messagesExchange) Failure(kind, message string) []byte {
	return errorBody(http.StatusBadGateway, "INTERNAL", message)
}

// finishReason is a Messages stop reason as Gemini's.
func finishReason(stop string) string {
	switch stop {
	case "max_tokens", "model_context_window_exceeded":
		return geminiwire.FinishMaxTokens
	case "refusal":
		return "SAFETY"
	}
	return geminiwire.FinishStop
}

// usageOf is Claude's usage as Gemini counts it: the cached input inside the
// prompt figure, not beside it. start is message_start's, which carries the
// input; end is message_delta's, which carries the output and may repeat
// the input.
func usageOf(start, end anthropicwire.Usage) *geminiwire.UsageMetadata {
	in := start
	if end.InputTokens > 0 || end.CacheReadInputTokens > 0 || end.CacheCreationInputTokens > 0 {
		in = end
	}
	prompt := in.InputTokens + in.CacheReadInputTokens + in.CacheCreationInputTokens
	return &geminiwire.UsageMetadata{
		PromptTokenCount:        prompt,
		CachedContentTokenCount: in.CacheReadInputTokens,
		CandidatesTokenCount:    end.OutputTokens,
		TotalTokenCount:         prompt + end.OutputTokens,
	}
}

// geminiStream turns a Messages stream into a Gemini one as it arrives.
type geminiStream struct {
	out api.FlushWriter
	tr  translation
	buf bytes.Buffer

	id       string
	model    string
	start    anthropicwire.Usage
	blocks   map[int]*openBlock
	finished bool
	// signature is the last thinking block, carried, waiting for the part
	// it goes on.
	signature string
}

type openBlock struct {
	block anthropicwire.ContentBlock
	json  strings.Builder
}

func newGeminiStream(out api.FlushWriter, tr translation) *geminiStream {
	return &geminiStream{out: out, tr: tr, blocks: map[int]*openBlock{}}
}

func (s *geminiStream) Write(p []byte) (int, error) {
	s.buf.Write(p)
	for {
		frame, ok := api.NextFrame(&s.buf)
		if !ok {
			return len(p), nil
		}
		name, data := api.FrameFields(frame)
		if len(data) > 0 {
			s.handle(name, data)
		}
	}
}

// event is any Messages event, in the fields some event uses.
type event struct {
	Type         string                      `json:"type"`
	Index        int                         `json:"index"`
	Message      *anthropicwire.Message      `json:"message"`
	ContentBlock *anthropicwire.ContentBlock `json:"content_block"`
	Delta        *anthropicwire.Delta        `json:"delta"`
	Usage        *anthropicwire.Usage        `json:"usage"`
	Error        *anthropicwire.Error        `json:"error"`
}

func (s *geminiStream) handle(name string, data []byte) {
	if s.finished {
		return
	}
	var ev event
	if json.Unmarshal(data, &ev) != nil {
		return
	}
	if name == "" {
		name = ev.Type
	}
	switch name {
	case anthropicwire.EventMessageStart:
		if ev.Message != nil {
			s.id = strings.TrimPrefix(ev.Message.ID, "msg_")
			s.model = ev.Message.Model
			s.start = ev.Message.Usage
		}
	case anthropicwire.EventContentBlockStart:
		if ev.ContentBlock != nil {
			s.blocks[ev.Index] = &openBlock{block: *ev.ContentBlock}
		}
	case anthropicwire.EventContentBlockDelta:
		b := s.blocks[ev.Index]
		if b == nil || ev.Delta == nil {
			return
		}
		switch ev.Delta.Type {
		case "text_delta":
			if ev.Delta.Text != "" {
				s.emit(s.response([]geminiwire.Part{s.sign(geminiwire.Part{Text: ev.Delta.Text})}, "", nil))
			}
		case "thinking_delta":
			b.block.Thinking += ev.Delta.Thinking
			if s.tr.thoughts && ev.Delta.Thinking != "" {
				s.emit(s.response([]geminiwire.Part{{Text: ev.Delta.Thinking, Thought: true}}, "", nil))
			}
		case "signature_delta":
			b.block.Signature = ev.Delta.Signature
		case "input_json_delta":
			b.json.WriteString(ev.Delta.PartialJSON)
		}
	case anthropicwire.EventContentBlockStop:
		b := s.blocks[ev.Index]
		delete(s.blocks, ev.Index)
		if b == nil {
			return
		}
		switch b.block.Type {
		case "thinking", "redacted_thinking":
			s.blockParts(b.block)
		case "tool_use":
			if raw := strings.TrimSpace(b.json.String()); raw != "" {
				b.block.Input = json.RawMessage(raw)
			}
			if parts := s.blockParts(b.block); len(parts) > 0 {
				s.emit(s.response(parts, "", nil))
			}
		}
	case anthropicwire.EventMessageDelta:
		stop := ""
		if ev.Delta != nil {
			stop = ev.Delta.StopReason
		}
		var end anthropicwire.Usage
		if ev.Usage != nil {
			end = *ev.Usage
		}
		var parts []geminiwire.Part
		if s.signature != "" {
			parts = append(parts, geminiwire.Part{Text: "", ThoughtSignature: s.signature})
			s.signature = ""
		}
		s.emit(s.response(parts, finishReason(stop), usageOf(s.start, end)))
		s.finished = true
	case anthropicwire.EventError:
		msg := "the upstream failed"
		code, status := http.StatusInternalServerError, "INTERNAL"
		if ev.Error != nil {
			msg = ev.Error.Message
			code = errorCode(ev.Error.Type)
			status = statusName(code)
		}
		s.emitError(code, status, msg)
	}
}

// errorCode is the HTTP status Anthropic's error type goes with.
func errorCode(kind string) int {
	switch kind {
	case "invalid_request_error":
		return 400
	case "authentication_error":
		return 401
	case "permission_error":
		return 403
	case "not_found_error":
		return 404
	case "rate_limit_error":
		return 429
	case "overloaded_error":
		return 503
	}
	return 500
}

// blockParts is a finished block as Gemini parts. A thinking block says
// nothing yet: it is carried on the part after it.
func (s *geminiStream) blockParts(b anthropicwire.ContentBlock) []geminiwire.Part {
	switch b.Type {
	case "thinking":
		if b.Signature != "" {
			s.signature = encodeThinking(carriedThinking{Thinking: b.Thinking, Signature: b.Signature})
		}
		if s.tr.thoughts && s.out == nil && b.Thinking != "" {
			return []geminiwire.Part{{Text: b.Thinking, Thought: true}}
		}
	case "redacted_thinking":
		if b.Data != "" {
			s.signature = encodeThinking(carriedThinking{Redacted: b.Data})
		}
	case "text":
		if b.Text != "" {
			return []geminiwire.Part{s.sign(geminiwire.Part{Text: b.Text})}
		}
	case "tool_use":
		args := b.Input
		if len(args) == 0 || string(args) == "null" {
			args = json.RawMessage(`{}`)
		}
		name := b.Name
		if own, ok := s.tr.tools[name]; ok {
			name = own
		}
		return []geminiwire.Part{s.sign(geminiwire.Part{
			FunctionCall: &geminiwire.FunctionCall{ID: b.ID, Name: name, Args: args},
		})}
	}
	return nil
}

// sign puts the carried thinking block on p, once.
func (s *geminiStream) sign(p geminiwire.Part) geminiwire.Part {
	if s.signature != "" {
		p.ThoughtSignature, s.signature = s.signature, ""
	}
	return p
}

func (s *geminiStream) response(parts []geminiwire.Part, finish string, usage *geminiwire.UsageMetadata) geminiwire.GenerateResponse {
	c := geminiwire.Candidate{FinishReason: finish}
	if len(parts) > 0 {
		c.Content = &geminiwire.Content{Role: "model", Parts: parts}
	}
	return geminiwire.GenerateResponse{
		Candidates:    []geminiwire.Candidate{c},
		UsageMetadata: usage,
		ModelVersion:  s.tr.model,
		ResponseID:    s.id,
	}
}

func (s *geminiStream) emit(r geminiwire.GenerateResponse) {
	raw, err := json.Marshal(r)
	if err != nil {
		return
	}
	s.write(raw)
}

func (s *geminiStream) emitError(code int, status, msg string) {
	s.write(errorBody(code, status, msg))
	s.finished = true
}

func (s *geminiStream) write(raw []byte) {
	var b bytes.Buffer
	b.WriteString("data: ")
	b.Write(raw)
	b.WriteString("\r\n\r\n")
	_, _ = s.out.Write(b.Bytes())
	s.out.Flush()
}

// Finish ends a stream that stopped before Claude finished it with an error
// chunk, which the SDK throws rather than taking a cut answer as whole.
func (s *geminiStream) Finish(cause error) {
	if s.finished {
		return
	}
	msg := "the upstream ended the stream before it completed"
	if cause != nil {
		msg = cause.Error()
	}
	s.emitError(http.StatusBadGateway, "UNAVAILABLE", msg)
}
