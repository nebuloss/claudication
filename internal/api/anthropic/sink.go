package anthropic

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"claudication/internal/anthropicwire"
	"claudication/internal/api"
)

// answer is one upstream's answer being said as Anthropic events: the ChatGPT
// backend's (converter, fromresponses.go) or Code Assist's (geminiConverter,
// fromgemini.go). The sink around it — sniffing, pinging, gathering for a
// caller that did not stream, the error envelope — is the same for both.
type answer interface {
	// handle reads one streamed event's data.
	handle(data []byte)
	// whole reads an answer that came as one JSON body rather than a
	// stream. It parses before it says anything, and returns the saying as
	// a function, so that an answer it cannot read is refused before a
	// streaming caller has been sent a 200.
	whole(body []byte) (func(), error)
	// fail ends the answer with an error event.
	fail(kind, message string)
	// live is whether the answer has started and not finished: the window
	// in which a ping is owed. done is whether it has finished.
	live() bool
	done() bool
}

// upstreamAnswer is what the sink needs to know about one upstream: how to
// read its answer, and how to read its refusal.
type upstreamAnswer struct {
	convert func(Translation, emitter) answer
	refusal func(status int, body []byte) (kind, message string)
}

// fromResponses reads the ChatGPT backend.
var fromResponses = upstreamAnswer{
	convert: func(tr Translation, out emitter) answer { return newConverter(tr, out) },
	refusal: backendError,
}

// responsesSink takes a translated request's answer and gives the caller a
// Messages one. Named for the first upstream it served, the ChatGPT
// backend; Code Assist's answer goes through it too (upstreamAnswer).
//
// Not api.NewReshapingSink, for two reasons measured on the ChatGPT backend.
// It tells a stream from JSON by Content-Type, and a streamed answer from
// that backend has none (internal/provider/openai, live-stream.txt,
// 2026-10-20). And every request goes up streaming — that backend refuses
// any other, and Code Assist is sent the streamed method too — so a caller
// that did not ask to stream is answered from a stream, gathered into one
// message, which the reshaping sink has no shape for.
type responsesSink struct {
	mu     sync.Mutex
	client http.ResponseWriter
	header http.Header
	tr     Translation
	up     upstreamAnswer

	status  int
	started bool
	// sse is set once a 200 is known to be a stream; json once it is known
	// to be a whole Responses object.
	sniffed bool
	json    bool
	buf     bytes.Buffer
	over    bool

	frames  bytes.Buffer // stream bytes not yet a whole frame
	conv    answer
	gather  *gatherer // the message being gathered, for a non-streaming caller
	stream  *sseOut   // the caller's stream, for a streaming one
	closed  bool
	pinging chan struct{}
}

// pingEvery is how long the caller's stream may go without a byte before a
// ping is sent. A reasoning model with no summary asked for is silent until
// its reasoning is done — minutes, at a high effort — and a client watching
// its stream for an idle gap fails the turn on one. Anthropic's own streams
// carry ping events for this; so does this one.
var pingEvery = 10 * time.Second

func newResponsesSink(w http.ResponseWriter, tr Translation, up upstreamAnswer) *responsesSink {
	tr.Body = nil
	return &responsesSink{client: w, header: http.Header{}, tr: tr, up: up}
}

func (s *responsesSink) Header() http.Header         { return s.header }
func (s *responsesSink) Unwrap() http.ResponseWriter { return s.client }

// Flush is the relay's flush, under the lock the pinger writes under: a
// ResponseController finds this before Unwrap, so the two never race.
func (s *responsesSink) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stream != nil {
		_ = http.NewResponseController(s.client).Flush()
	}
}

func (s *responsesSink) WriteHeader(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeHeader(status)
}

func (s *responsesSink) writeHeader(status int) {
	if s.started {
		return
	}
	s.started = true
	s.status = status
}

func (s *responsesSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		s.writeHeader(http.StatusOK)
	}
	if s.status != http.StatusOK || s.json {
		s.hold(p)
		return len(p), nil
	}
	if !s.sniffed {
		trimmed := bytes.TrimLeft(p, " \t\r\n")
		if len(trimmed) == 0 {
			return len(p), nil
		}
		s.sniffed = true
		if trimmed[0] == '{' || trimmed[0] == '[' {
			// A whole answer rather than a stream: not what either backend
			// sends a streaming request, but readable all the same.
			s.json = true
			s.hold(p)
			return len(p), nil
		}
		s.begin()
	}
	s.frames.Write(p)
	for {
		frame, ok := api.NextFrame(&s.frames)
		if !ok {
			break
		}
		// The event name is not needed: every payload names its type.
		if _, data := api.FrameFields(frame); len(data) > 0 {
			s.conv.handle(data)
		}
	}
	return len(p), nil
}

func (s *responsesSink) hold(p []byte) {
	if s.buf.Len()+len(p) > maxHeld {
		s.over = true
		return
	}
	s.buf.Write(p)
}

// maxHeld bounds a refusal or a whole answer held to be translated.
const maxHeld = 32 << 20

// begin sets up the answer: a stream to the caller who asked for one, with
// its headers sent now so the first event goes out the moment it is made,
// or a message being gathered.
func (s *responsesSink) begin() {
	if s.tr.Stream {
		s.forwardHeaders()
		h := s.client.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		s.client.WriteHeader(http.StatusOK)
		s.stream = &sseOut{w: s.client, last: time.Now()}
		s.conv = s.up.convert(s.tr, s.stream)
		// Not once the answer is closed: a whole answer, or none, is
		// written by Close itself, with nobody left to stop a pinger.
		if !s.closed {
			s.pinging = make(chan struct{})
			// The interval is read here, on the caller's goroutine, not in
			// the pinger: a pinger outlives the request it was started for
			// by up to a tick, and a test that changes pingEvery raced it.
			go s.ping(s.pinging, pingEvery)
		}
		return
	}
	s.gather = &gatherer{}
	s.conv = s.up.convert(s.tr, s.gather)
}

// ping keeps the caller's stream from going idle until the answer is done.
func (s *responsesSink) ping(done chan struct{}, every time.Duration) {
	t := time.NewTicker(every / 2)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			s.mu.Lock()
			if !s.closed && s.conv.live() && time.Since(s.stream.last) >= every {
				s.stream.emit(anthropicwire.EventPing, map[string]any{"type": anthropicwire.EventPing})
			}
			s.mu.Unlock()
		}
	}
}

// Close writes what the answer still owes: the end of a stream however it
// stopped, a gathered message, or a refusal in Anthropic's envelope.
func (s *responsesSink) Close(cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.pinging != nil {
		close(s.pinging)
	}
	if !s.started {
		// Nothing came back; the gateway answers, knowing why.
		return
	}
	if s.status != http.StatusOK {
		kind, msg := s.up.refusal(s.status, s.buf.Bytes())
		s.forwardHeaders()
		s.writeError(s.status, kind, msg)
		return
	}
	if s.over {
		s.writeError(http.StatusBadGateway, "api_error", "the upstream answer was too large to translate")
		return
	}
	if s.json {
		// Read before anything is said: an answer that cannot be read is a
		// 502, which a streaming caller could no longer be sent once begin
		// has answered 200.
		if _, err := s.up.convert(s.tr, discard{}).whole(s.buf.Bytes()); err != nil {
			s.writeError(http.StatusBadGateway, "api_error", "could not read the upstream answer: "+err.Error())
			return
		}
		s.begin()
		say, _ := s.conv.whole(s.buf.Bytes())
		say()
	}
	if s.conv == nil {
		// A 200 with no body at all.
		s.begin()
	}
	if rest := bytes.TrimSpace(s.frames.Bytes()); len(rest) > 0 {
		// A last frame with no blank line after it: the connection cut
		// after the event that ended the turn is still that event.
		if _, data := api.FrameFields(rest); len(data) > 0 {
			s.conv.handle(data)
		}
	}
	if !s.conv.done() {
		msg := "the upstream ended the stream before it completed"
		if cause != nil {
			msg = cause.Error()
		}
		s.conv.fail("api_error", msg)
	}
	if s.gather != nil {
		s.forwardHeaders()
		if s.gather.err != nil {
			s.writeError(kindStatus(s.gather.err.Type), s.gather.err.Type, s.gather.err.Message)
			return
		}
		body, err := json.Marshal(s.gather.message())
		if err != nil {
			s.writeError(http.StatusBadGateway, "api_error", "could not encode the answer: "+err.Error())
			return
		}
		s.client.Header().Set("Content-Type", "application/json")
		s.client.WriteHeader(http.StatusOK)
		_, _ = s.client.Write(body)
	}
}

func (s *responsesSink) writeError(status int, kind, message string) {
	body, _ := json.Marshal(anthropicwire.ErrorEnvelope{
		Type: "error", Error: anthropicwire.Error{Type: kind, Message: message},
	})
	s.client.Header().Set("Content-Type", "application/json")
	s.client.WriteHeader(status)
	_, _ = s.client.Write(body)
}

// forwardHeaders passes on the one header of the backend's a Messages client
// acts on: Retry-After, on a refusal it may wait out. The rest — the
// x-codex-* quota family, the turn state — describe ChatGPT's protocol, and
// the gateway reads them itself (internal/provider/openai).
func (s *responsesSink) forwardHeaders() {
	if v := s.header.Get("Retry-After"); v != "" {
		s.client.Header().Set("Retry-After", v)
	}
}

// discard is an emitter that says nothing: what reads an answer to see
// whether it can be read.
type discard struct{}

func (discard) emit(string, any) {}

// sseOut writes the Anthropic events as a stream, each flushed as it is
// made: a frame in a buffer is a frame the client's idle watch never saw.
type sseOut struct {
	w    http.ResponseWriter
	last time.Time
}

func (o *sseOut) emit(event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	var b bytes.Buffer
	b.WriteString("event: ")
	b.WriteString(event)
	b.WriteString("\ndata: ")
	b.Write(data)
	b.WriteString("\n\n")
	_, _ = o.w.Write(b.Bytes())
	_ = http.NewResponseController(o.w).Flush()
	o.last = time.Now()
}

// gatherer builds one message from the events, the way the Anthropic SDKs
// accumulate a stream, for a caller that did not ask to stream.
type gatherer struct {
	start   messageStart
	blocks  []map[string]any
	partial map[int]*strings.Builder
	stop    any
	usage   map[string]any
	err     *anthropicwire.Error
}

func (g *gatherer) emit(event string, payload any) {
	p, _ := payload.(map[string]any)
	switch event {
	case anthropicwire.EventMessageStart:
		g.start, _ = p["message"].(messageStart)
	case anthropicwire.EventContentBlockStart:
		block, _ := p["content_block"].(map[string]any)
		copied := map[string]any{}
		for k, v := range block {
			copied[k] = v
		}
		g.blocks = append(g.blocks, copied)
	case anthropicwire.EventContentBlockDelta:
		i, _ := p["index"].(int)
		d, _ := p["delta"].(map[string]any)
		if i < 0 || i >= len(g.blocks) {
			return
		}
		b := g.blocks[i]
		switch d["type"] {
		case "text_delta":
			b["text"] = b["text"].(string) + d["text"].(string)
		case "thinking_delta":
			b["thinking"] = b["thinking"].(string) + d["thinking"].(string)
		case "signature_delta":
			b["signature"] = d["signature"]
		case "input_json_delta":
			if g.partial == nil {
				g.partial = map[int]*strings.Builder{}
			}
			if g.partial[i] == nil {
				g.partial[i] = &strings.Builder{}
			}
			g.partial[i].WriteString(d["partial_json"].(string))
		}
	case anthropicwire.EventContentBlockStop:
		i, _ := p["index"].(int)
		if sb := g.partial[i]; sb != nil && i < len(g.blocks) {
			if raw := json.RawMessage(sb.String()); json.Valid(raw) {
				g.blocks[i]["input"] = raw
			}
		}
	case anthropicwire.EventMessageDelta:
		d, _ := p["delta"].(map[string]any)
		g.stop = d["stop_reason"]
		g.usage, _ = p["usage"].(map[string]any)
	case anthropicwire.EventError:
		e, _ := p["error"].(map[string]string)
		g.err = &anthropicwire.Error{Type: e["type"], Message: e["message"]}
	}
}

func (g *gatherer) message() map[string]any {
	usage := map[string]any{"input_tokens": 0, "output_tokens": 0}
	for k, v := range g.usage {
		usage[k] = v
	}
	content := []any{}
	for _, b := range g.blocks {
		content = append(content, b)
	}
	return map[string]any{
		"id": g.start.ID, "type": "message", "role": "assistant", "model": g.start.Model,
		"content": content, "stop_reason": g.stop, "stop_sequence": nil, "usage": usage,
	}
}

var _ api.Sink = (*responsesSink)(nil)
