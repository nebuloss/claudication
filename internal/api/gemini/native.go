package gemini

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"claudication/internal/api"
	"claudication/internal/geminiwire"
)

// Gemini to a Google account: Code Assist takes the Gemini API's request
// wrapped, as gemini-cli wraps its own (converter.ts toGenerateContentRequest):
//
//	{"model": <the path's>, "request": <the caller's body, as it came>}
//
// The project is stamped in per attempt by the provider. The request's
// fields are Vertex's names, which are the Gemini API's, so the body goes in
// as the caller wrote it — its thought signatures with it, which is what
// keeps Gemini 3's reasoning across a tool loop.
//
// The answer comes back wrapped too, each event {"response": <a Gemini API
// chunk>, "traceId": …}, and goes out unwrapped: as events to a caller that
// streamed, gathered into one response for generateContent — the backend is
// always asked for a stream, as for the other dialects. Errors are already
// google.rpc.Status, the shape the caller reads, and pass as they came.

type nativeExchange struct {
	body   []byte
	model  string
	stream bool
}

func newNative(e envelope) (api.Exchange, error) {
	body, err := json.Marshal(struct {
		Model   string          `json:"model"`
		Request json.RawMessage `json:"request"`
	}{Model: e.Model, Request: e.Request})
	if err != nil {
		return nil, err
	}
	return &nativeExchange{body: body, model: e.Model, stream: e.Method == "streamGenerateContent"}, nil
}

func (e *nativeExchange) Request() []byte {
	body := e.body
	e.body = nil
	return body
}
func (e *nativeExchange) Model() string         { return e.model }
func (e *nativeExchange) Streaming() bool       { return e.stream }
func (e *nativeExchange) Headers(h http.Header) { dropDialectHeaders(h) }

func (e *nativeExchange) Sink(w http.ResponseWriter) api.Sink {
	return &nativeSink{client: w, header: http.Header{}, stream: e.stream}
}

// nativeSink unwraps Code Assist's answer.
type nativeSink struct {
	mu     sync.Mutex
	client http.ResponseWriter
	header http.Header
	stream bool

	status  int
	started bool
	sse     bool // the answer is a stream, sent on as one
	headed  bool // the caller's headers are out
	held    bytes.Buffer
	frames  bytes.Buffer
	gather  gathered
	// failed is that the answer reported an error; ended that a streamed
	// one finished.
	failed bool
	ended  bool
}

func (s *nativeSink) Header() http.Header         { return s.header }
func (s *nativeSink) Unwrap() http.ResponseWriter { return s.client }

func (s *nativeSink) WriteHeader(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		s.started, s.status = true, status
	}
}

func (s *nativeSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		s.started, s.status = true, http.StatusOK
	}
	if s.status != http.StatusOK {
		s.hold(p)
		return len(p), nil
	}
	if !s.sse && s.held.Len() == 0 {
		trimmed := bytes.TrimLeft(p, " \t\r\n")
		if len(trimmed) == 0 {
			return len(p), nil
		}
		// A whole answer rather than a stream is read at Close.
		if trimmed[0] == '{' || trimmed[0] == '[' {
			s.hold(p)
			return len(p), nil
		}
		s.sse = true
	}
	if !s.sse {
		s.hold(p)
		return len(p), nil
	}
	s.frames.Write(p)
	for {
		frame, ok := api.NextFrame(&s.frames)
		if !ok {
			break
		}
		if _, data := api.FrameFields(frame); len(data) > 0 {
			s.event(data)
		}
	}
	return len(p), nil
}

func (s *nativeSink) hold(p []byte) {
	if s.held.Len()+len(p) <= maxHeld {
		s.held.Write(p)
	}
}

// maxHeld bounds a refusal or a whole answer held to be unwrapped.
const maxHeld = 32 << 20

// wrapped is one event of Code Assist's stream: a chunk, or an error.
type wrapped struct {
	Response *json.RawMessage `json:"response"`
	TraceID  string           `json:"traceId"`
	Error    *json.RawMessage `json:"error"`
}

// event sends on or gathers one wrapped event.
func (s *nativeSink) event(data []byte) {
	var w wrapped
	if json.Unmarshal(data, &w) != nil {
		return
	}
	switch {
	case w.Error != nil:
		s.failed = true
		if s.stream {
			s.send(append(append([]byte(`{"error":`), *w.Error...), '}'))
		} else {
			s.gather.err = append(append([]byte(`{"error":`), *w.Error...), '}')
		}
	case w.Response != nil:
		chunk := []byte(*w.Response)
		if w.TraceID != "" {
			chunk = withResponseID(chunk, w.TraceID)
		}
		if s.stream {
			s.ended = s.ended || finishes(chunk)
			s.send(chunk)
		} else {
			s.gather.add(chunk)
		}
	}
}

// finishes reports whether a chunk ends the answer: a finish reason, or a
// prompt blocked before any.
func finishes(chunk []byte) bool {
	var r geminiwire.GenerateResponse
	if json.Unmarshal(chunk, &r) != nil {
		return false
	}
	if r.PromptFeedback != nil && r.PromptFeedback.BlockReason != "" {
		return true
	}
	return len(r.Candidates) > 0 && r.Candidates[0].FinishReason != ""
}

// withResponseID sets a chunk's responseId to the trace id when it has none,
// as the Gemini API's chunks carry one.
func withResponseID(chunk []byte, id string) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(chunk, &m) != nil {
		return chunk
	}
	if _, ok := m["responseId"]; ok {
		return chunk
	}
	m["responseId"], _ = json.Marshal(id)
	out, err := json.Marshal(m)
	if err != nil {
		return chunk
	}
	return out
}

// send writes one chunk to a streaming caller, the headers first.
func (s *nativeSink) send(chunk []byte) {
	if !s.headed {
		s.headed = true
		s.forwardHeaders()
		h := s.client.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		s.client.WriteHeader(http.StatusOK)
	}
	var b bytes.Buffer
	b.WriteString("data: ")
	b.Write(chunk)
	b.WriteString("\r\n\r\n")
	_, _ = s.client.Write(b.Bytes())
	_ = http.NewResponseController(s.client).Flush()
}

// forwardHeaders passes on what a caller acts on: Retry-After.
func (s *nativeSink) forwardHeaders() {
	if v := s.header.Get("Retry-After"); v != "" {
		s.client.Header().Set("Retry-After", v)
	}
}

func (s *nativeSink) writeJSON(status int, body []byte) {
	s.forwardHeaders()
	s.client.Header().Set("Content-Type", "application/json")
	s.client.WriteHeader(status)
	_, _ = s.client.Write(body)
}

// Close writes what the answer still owes: a refusal as it came, a whole
// answer unwrapped, a gathered one, or the end of a stream that stopped
// before the backend finished it.
func (s *nativeSink) Close(cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		return
	}
	if s.status != http.StatusOK {
		s.writeJSON(s.status, s.held.Bytes())
		return
	}
	if rest := bytes.TrimSpace(s.frames.Bytes()); len(rest) > 0 {
		if _, data := api.FrameFields(rest); len(data) > 0 {
			s.event(data)
		}
	}
	if !s.sse {
		// A whole answer: one envelope, or an array of them.
		trimmed := bytes.TrimSpace(s.held.Bytes())
		var events []json.RawMessage
		if len(trimmed) > 0 && trimmed[0] == '[' {
			_ = json.Unmarshal(trimmed, &events)
		} else if len(trimmed) > 0 {
			events = []json.RawMessage{trimmed}
		}
		for _, ev := range events {
			s.event(ev)
		}
	}
	if s.stream {
		msg := "the upstream ended the stream before it completed"
		if cause != nil {
			msg = cause.Error()
		}
		switch {
		case !s.headed:
			s.writeJSON(http.StatusBadGateway, errorBody(http.StatusBadGateway, "UNAVAILABLE", msg))
		case !s.ended && !s.failed:
			// An error chunk, which the SDK throws, rather than a cut
			// answer it would take as whole.
			s.send(errorBody(http.StatusBadGateway, "UNAVAILABLE", msg))
		}
		return
	}
	if s.gather.err != nil {
		s.writeJSON(http.StatusBadGateway, s.gather.err)
		return
	}
	body, err := s.gather.response()
	if err != nil {
		msg := "could not read the upstream answer"
		if cause != nil {
			msg = cause.Error()
		}
		s.writeJSON(http.StatusBadGateway, errorBody(http.StatusBadGateway, "INTERNAL", msg))
		return
	}
	s.writeJSON(http.StatusOK, body)
}

// gathered is a streamed answer gathered into one, as @google/genai
// accumulates a stream: the parts in order, a run of plain text parts
// joined, and the last chunk's finish reason, usage and model.
type gathered struct {
	parts  []geminiwire.Part
	finish string
	usage  *geminiwire.UsageMetadata
	model  string
	id     string
	seen   bool
	err    []byte
}

func (g *gathered) add(chunk []byte) {
	var r geminiwire.GenerateResponse
	if json.Unmarshal(chunk, &r) != nil {
		return
	}
	g.seen = true
	if r.UsageMetadata != nil {
		g.usage = r.UsageMetadata
	}
	g.model = firstOf(r.ModelVersion, g.model)
	g.id = firstOf(r.ResponseID, g.id)
	if r.PromptFeedback != nil && r.PromptFeedback.BlockReason != "" {
		g.finish = "SAFETY"
	}
	if len(r.Candidates) == 0 {
		return
	}
	c := r.Candidates[0]
	if c.FinishReason != "" {
		g.finish = c.FinishReason
	}
	if c.Content == nil {
		return
	}
	for _, p := range c.Content.Parts {
		n := len(g.parts)
		plain := p.FunctionCall == nil && p.FunctionResponse == nil && p.InlineData == nil && p.FileData == nil
		if plain && n > 0 {
			last := &g.parts[n-1]
			lastPlain := last.FunctionCall == nil && last.FunctionResponse == nil && last.InlineData == nil && last.FileData == nil
			if lastPlain && last.Thought == p.Thought && p.ThoughtSignature == "" {
				last.Text += p.Text
				continue
			}
		}
		if plain && p.Text == "" && p.ThoughtSignature == "" {
			continue
		}
		g.parts = append(g.parts, p)
	}
}

func (g *gathered) response() ([]byte, error) {
	if !g.seen {
		return nil, errNoAnswer
	}
	c := geminiwire.Candidate{FinishReason: g.finish}
	if len(g.parts) > 0 {
		c.Content = &geminiwire.Content{Role: "model", Parts: g.parts}
	}
	return json.Marshal(geminiwire.GenerateResponse{
		Candidates: []geminiwire.Candidate{c}, UsageMetadata: g.usage, ModelVersion: g.model, ResponseID: g.id,
	})
}

type answerError string

func (e answerError) Error() string { return string(e) }

const errNoAnswer = answerError("the upstream sent no answer")

func firstOf(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
