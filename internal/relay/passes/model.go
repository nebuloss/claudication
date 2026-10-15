package passes

import (
	"bytes"
	"encoding/json"
	"net/http"

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

// modelKey is the bytes every model field in an answer starts with. The
// upstream's JSON is compact; a model key inside text or tool input is
// escaped (\"model\") and does not match.
var modelKey = []byte(`"model":"`)

// ModelRenamer is a ResponseWriter that rewrites the value of every "model"
// field in what passes through it to one name, for an answer to a virtual
// model.
//
// Every one, not the first: a streamed Anthropic answer names the model once,
// in message_start, but the Responses dialect names it in several events. It
// never holds a whole event back. Only a tail that could be the start of a
// model field, or a model value not yet closed, waits for the next write —
// a few bytes, and only in the moment the field is split across two writes.
type ModelRenamer struct {
	http.ResponseWriter
	to      []byte
	pending []byte
}

// NewModelRenamer wraps w so the answer names the model as name.
func NewModelRenamer(w http.ResponseWriter, name string) *ModelRenamer {
	quoted, _ := json.Marshal(name)
	// The value without its quotes: the opening one is in modelKey, and the
	// closing one is left where the original value's was.
	return &ModelRenamer{ResponseWriter: w, to: quoted[1 : len(quoted)-1]}
}

func (m *ModelRenamer) Write(p []byte) (int, error) {
	m.pending = append(m.pending, p...)
	out, rest := renameModels(m.pending, m.to)
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

// Finish writes anything still held back, as it was. Called when the answer
// is over: a field still open then is not one this could have finished.
func (m *ModelRenamer) Finish() {
	if len(m.pending) > 0 {
		_, _ = m.ResponseWriter.Write(m.pending)
		m.pending = nil
	}
}

// renameModels rewrites every complete model field in b, and splits off the
// tail that might still become one.
func renameModels(b, to []byte) (out, rest []byte) {
	var buf bytes.Buffer
	for {
		i := bytes.Index(b, modelKey)
		if i < 0 {
			break
		}
		start := i + len(modelKey)
		end := bytes.IndexByte(b[start:], '"')
		if end < 0 {
			// The value is not closed yet: keep from the key on.
			buf.Write(b[:i])
			return buf.Bytes(), b[i:]
		}
		buf.Write(b[:i])
		buf.Write(modelKey)
		buf.Write(to)
		b = b[start+end:]
	}
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
