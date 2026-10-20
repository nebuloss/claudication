package anthropic

import (
	"encoding/json"
	"net/http"

	"claudication/internal/anthropicwire"
	"claudication/internal/api"
	"claudication/internal/request"
)

// ID names this surface in the settings table, the admin UI and the log.
const ID = "anthropic"

// API is the Anthropic Messages surface.
type API struct {
	// reasoning keeps a GPT model's reasoning for the next turn of a caller
	// that does not carry it itself (reasoningstore.go). Nil keeps none.
	reasoning *reasoningStore
}

// New returns the surface. Relaying a request unchanged takes nothing to
// configure; translating one for ChatGPT keeps its reasoning between turns.
func New() API { return API{reasoning: newReasoningStore(maxReasoningBytes)} }

func (API) ID() string    { return ID }
func (API) Title() string { return "Anthropic Messages API" }

func (API) Routes() []string {
	return []string{"/v1/messages", "/v1/messages/count_tokens", "/v1/models"}
}

// Decode reads the model and stream flag and hands the body straight back.
//
// It deliberately does not decode the whole body. Peek reads the three fields
// the gateway needs; decoding the envelope into raw values used to copy the
// transcript twice per request just to learn a model name.
func (API) Decode(body []byte) (api.Exchange, error) {
	return exchange{body: body, prologue: request.Peek(body)}, nil
}

// WriteError writes Anthropic's own error envelope, which is what a Claude
// Code client parses. The kinds are already Anthropic's types, so the mapping
// is the identity.
func (API) WriteError(w http.ResponseWriter, status int, kind api.ErrorKind, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    string(kind),
			"message": message,
		},
	})
}

// ModelEvents is message_start alone: it is the one event whose message
// names the model, and its content is still empty when it does.
func (API) ModelEvents() []string { return []string{anthropicwire.EventMessageStart} }

// ConversationID reads the chat id out of a request in this dialect.
//
// Three clients speak Anthropic to this gateway and they do not agree on a
// header, so all of them are checked, most specific first:
//
//	X-Claude-Code-Session-Id  Claude Code, and only it
//	x-session-id              opencode and crush, both of which also send
//	                          x-session-affinity with the same value
//
// The body is the last resort and normally does not run, because Claude Code —
// the only client that puts the id in a body at all — sends the header too.
func (API) ConversationID(h http.Header, body []byte) string {
	if id := api.HeaderID(h, "X-Claude-Code-Session-Id", "X-Session-Id"); id != "" {
		return id
	}
	return api.SessionFromMetadata(body)
}

type exchange struct {
	body     []byte
	prologue request.Prologue
}

func (e exchange) Request() []byte { return e.body }
func (e exchange) Model() string   { return e.prologue.Model }
func (e exchange) Streaming() bool { return e.prologue.Stream }

// Prologue is what Decode already read. The body going upstream is the one it
// read it from, so the relay need not parse it a second time.
func (e exchange) Prologue() request.Prologue { return e.prologue }

// Headers does nothing, which is the point: the caller already speaks what the
// upstream speaks, so every header it sent is meaningful there and the relay's
// contract is to preserve them.
func (exchange) Headers(http.Header) {}

func (exchange) Sink(w http.ResponseWriter) api.Sink { return api.Passthrough(w) }
