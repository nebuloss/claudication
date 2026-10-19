package passes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"claudication/internal/request"
)

// Model roles: the seventh exception.
//
// Every client config the gateway writes asks for claude-large and
// claude-small, two roles the operator binds to real models in the gateway
// (store.RoleLarge, store.RoleSmall) — so moving every client from one model
// to the next is one edit here rather than one per client. The upstream knows
// only real ids, so a request naming a role cannot succeed as sent: the model
// field has to say the real one.
//
// Unlike the six in Default it is not a repair of a shape the upstream
// rejects, and it does not run on every request. It runs only when a request
// names a role, it changes exactly one value — the model — and the gateway
// has decided which real model that is before it runs (the key's binding,
// else the gateway's, else the newest of the role's family). So it is a
// function the gateway calls with that decision, not a step in the pipeline.
//
// The answer says the role back: the client asked for claude-large, and an
// answer naming claude-opus-5-5 is a name it was never configured with.
// ModelRenamer does that on the way out. The request log keeps both.

// SetModel puts model where body's model field is. ok is false when the body
// has no top-level model to replace, which a request that got this far has.
func SetModel(body []byte, model string) (out []byte, ok bool) {
	value, err := json.Marshal(model)
	if err != nil {
		return nil, false
	}
	return request.ReplaceTopLevel(body, "model", value)
}

// modelKey is the bytes a model field in a streamed event starts with. Every
// stream that reaches the renamer is compact JSON: the upstream's own, or a
// dialect's json.Marshal.
var modelKey = []byte(`"model":"`)

// eventField starts the line that names an SSE event.
var eventField = []byte("event:")

// ModelRenamer is a ResponseWriter that says a model role back in the answer
// to a request that named one: the model the answer reports becomes the
// role's name. Nothing else in the answer changes.
//
// Only the answer's own model field, because a "model" key can also be the
// model's output. A tool call whose input has a model argument arrives, in a
// non-streaming Anthropic answer, as a raw JSON object inside content — not a
// string, so its key is not escaped — and rewriting every match there renamed
// a caller's tool argument to the role. So:
//
//   - a JSON answer is held whole and only its top-level model replaced, which
//     is where both dialects put it. The client is waiting for the whole body
//     anyway, and nothing in it is sent sooner by sending part of it.
//   - a stream is renamed only in the events its dialect names as carrying the
//     model (api.Protocol.ModelEvents: message_start for Anthropic, the
//     response.* lifecycle events for Responses), and only in the first model
//     field of each — which is message.model or response.model, because in
//     message_start content is still empty and in a Responses event the keys
//     are sorted and model precedes output. Tool input in a stream is a string
//     (partial_json, arguments) and could not match anyway.
//
// A stream is never held back by more than a line it has not finished: the
// name of an event, or the bytes from a model key whose value is not yet
// closed, wait for the next write. Every other byte goes out with the write
// that carried it, so streaming is not delayed.
type ModelRenamer struct {
	http.ResponseWriter
	to     []byte // the name, without its quotes
	quoted []byte // the name as a JSON string
	events map[string]bool

	decided bool
	stream  bool
	pending []byte

	// midLine is that the next streamed byte continues a line already sent.
	midLine bool
	// renaming is that the current event carries the model and its field has
	// not been rewritten yet.
	renaming bool
}

// NewModelRenamer wraps w so the answer names the model as name. events are
// the streamed events that carry the model, as the caller's dialect names
// them.
func NewModelRenamer(w http.ResponseWriter, name string, events []string) *ModelRenamer {
	quoted, _ := json.Marshal(name)
	m := &ModelRenamer{
		ResponseWriter: w,
		// The value without its quotes: the opening one is in modelKey, and
		// the closing one is left where the original value's was.
		to:     quoted[1 : len(quoted)-1],
		quoted: quoted,
		events: make(map[string]bool, len(events)),
	}
	for _, e := range events {
		m.events[e] = true
	}
	return m
}

// WriteHeader drops a Content-Length, which describes a body whose model name
// is about to change length.
func (m *ModelRenamer) WriteHeader(status int) {
	m.Header().Del("Content-Length")
	m.ResponseWriter.WriteHeader(status)
}

func (m *ModelRenamer) Write(p []byte) (int, error) {
	if !m.decided {
		// Decided on the first byte, by the header already set for it: the
		// shape of the answer, not of the request, is what says where the
		// model is. A streaming request refused before its stream began is
		// answered with JSON.
		m.decided = true
		m.stream = strings.Contains(m.Header().Get("Content-Type"), "text/event-stream")
		m.Header().Del("Content-Length")
	}
	m.pending = append(m.pending, p...)
	if !m.stream {
		return len(p), nil
	}
	out, rest := m.renameEvents(m.pending)
	m.pending = append(m.pending[:0], rest...)
	if len(out) > 0 {
		if _, err := m.ResponseWriter.Write(out); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Flush sends what can be sent and flushes the writer beneath.
func (m *ModelRenamer) Flush() {
	if f, ok := m.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the writer beneath.
func (m *ModelRenamer) Unwrap() http.ResponseWriter { return m.ResponseWriter }

// Finish writes what is still held: a JSON answer, renamed, or the tail of a
// stream as it was — a field still open when the answer is over is not one
// this could have finished.
func (m *ModelRenamer) Finish() {
	if len(m.pending) == 0 {
		return
	}
	out := m.pending
	if !m.stream {
		// An answer without a top-level model — a refusal, a count — goes as
		// it came.
		if renamed, ok := request.ReplaceTopLevel(m.pending, "model", m.quoted); ok {
			out = renamed
		}
	}
	_, _ = m.ResponseWriter.Write(out)
	m.pending = nil
}

// renameEvents rewrites the model field of each event that carries one, and
// splits off the tail that cannot be decided yet.
func (m *ModelRenamer) renameEvents(b []byte) (out, rest []byte) {
	var buf bytes.Buffer
	for len(b) > 0 {
		if !m.midLine {
			// A line's start: an event's name, or the blank line ending one.
			if len(b) < len(eventField) && bytes.HasPrefix(eventField, b) {
				return buf.Bytes(), b
			}
			if bytes.HasPrefix(b, eventField) {
				nl := bytes.IndexByte(b, '\n')
				if nl < 0 {
					return buf.Bytes(), b
				}
				m.renaming = m.events[string(bytes.TrimSpace(b[len(eventField):nl]))]
				buf.Write(b[:nl+1])
				b = b[nl+1:]
				continue
			}
			if b[0] == '\n' {
				m.renaming = false
				buf.WriteByte('\n')
				b = b[1:]
				continue
			}
			m.midLine = true
		}

		nl := bytes.IndexByte(b, '\n')
		line := b
		if nl >= 0 {
			line = b[:nl+1]
		}
		if m.renaming {
			if i := bytes.Index(line, modelKey); i >= 0 {
				start := i + len(modelKey)
				if end := bytes.IndexByte(line[start:], '"'); end >= 0 {
					buf.Write(b[:i])
					buf.Write(modelKey)
					buf.Write(m.to)
					b = b[start+end:]
					m.renaming = false
					continue
				}
				if nl < 0 {
					// The value is not closed yet: keep from the key on.
					buf.Write(b[:i])
					return buf.Bytes(), b[i:]
				}
			} else if nl < 0 {
				// Hold back a tail that is a start of the key, and nothing else.
				keep := 0
				for n := min(len(modelKey)-1, len(b)); n > 0; n-- {
					if bytes.HasSuffix(b, modelKey[:n]) {
						keep = n
						break
					}
				}
				buf.Write(b[:len(b)-keep])
				return buf.Bytes(), b[len(b)-keep:]
			}
		}
		buf.Write(line)
		b = b[len(line):]
		if nl >= 0 {
			m.midLine = false
		}
	}
	return buf.Bytes(), b
}
