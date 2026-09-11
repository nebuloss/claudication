package openai

import (
	"encoding/json"
	"net/http"
	"strings"

	"claudication/internal/api"
)

// ID names this surface in the settings table, the admin UI and the log.
//
// "openai" rather than "responses" because the surface is the vendor's API,
// not one route on it: a chat-completions route would join this same surface
// and share its switch.
const ID = "openai"

// API serves the OpenAI Responses API, which is what Codex CLI speaks.
//
// Only /v1/responses, and only because that is all Codex has: `/responses`
// appears 41 times in the binary and `chat_completions` not at all. Serving
// /v1/chat/completions would help other OpenAI clients — DeepSeek's, among
// many — and would do nothing whatsoever for Codex, so it is not here until
// something asks for it.
//
// See doc.go for what Codex actually sends, what it does with the answer, and
// the measurements behind both.
type API struct {
	// model stands in when the caller names something the upstream has never
	// heard of, which is every default Codex install.
	model string
	// maxTokens is the ceiling to request when the caller names none. Codex
	// never sends max_output_tokens and Anthropic requires max_tokens, so
	// without this every request is refused.
	maxTokens int
}

// New returns the surface configured for this gateway.
func New(model string, maxTokens int) API {
	return API{model: model, maxTokens: maxTokens}
}

func (API) ID() string { return ID }
func (API) Title() string { return "OpenAI Responses API" }
func (API) Routes() []string { return []string{"/v1/responses"} }

func (a API) Decode(body []byte) (api.Exchange, error) {
	req, err := ResponsesToAnthropic(body, Options{
		Model:     a.model,
		MaxTokens: a.maxTokens,
	})
	if err != nil {
		return nil, err
	}
	return &exchange{req: req}, nil
}

// WriteError writes OpenAI's error envelope. A Codex that cannot parse an
// error reports a bare stream failure instead, which names nothing.
func (API) WriteError(w http.ResponseWriter, status int, kind, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(ErrorEnvelope(kind, message))
}

// exchange is one Responses request in flight. It is also the api.Reshaper for
// its own answer, so the tool-origin map it carries is in scope for both
// directions and cannot drift apart from the request that produced it.
type exchange struct {
	req Request
}

func (e *exchange) Request() []byte { return e.req.Body }
func (e *exchange) Model() string   { return e.req.Model }
func (e *exchange) Stream() bool    { return e.req.Stream }

func (e *exchange) Sink(w http.ResponseWriter) api.Sink {
	return api.NewReshapingSink(w, e)
}

// Stream, Complete, Refusal and Failure are the four conversions api's
// reshaping sink needs; it decides which of them an answer calls for.

func (e *exchange) Stream(out api.FlushWriter) api.StreamWriter {
	return NewStream(out, e.req)
}

func (e *exchange) Complete(body []byte) ([]byte, error) {
	return AnthropicToResponses(body, e.req)
}

func (e *exchange) Refusal(_ int, body []byte) []byte {
	kind, message := anthropicErrorFields(body)
	return ErrorEnvelope(kind, message)
}

func (e *exchange) Failure(kind, message string) []byte {
	return ErrorEnvelope(kind, message)
}

// dialectHeaders are the request headers that only mean something to OpenAI.
//
// They describe a request that no longer exists by the time this one goes
// upstream: the body has been rebuilt as Anthropic, and these would be the
// only trace left of the protocol it used to be. Anthropic ignores headers it
// does not know, so none of this is load-bearing — but the subscription
// backend has refused requests over their content three times now, every one
// found by bisection and none by reasoning, so sending it something that looks
// like nothing else on earth is a risk taken for no gain.
var dialectHeaders = []string{
	"Openai-Beta",
	"Openai-Organization",
	"Openai-Project",
	"Originator",
	"Session_id",
	"Conversation_id",
}

func (*exchange) Headers(h http.Header) {
	for _, name := range dialectHeaders {
		h.Del(name)
	}
	// The OpenAI SDKs tag every request with a family of these. They are
	// telemetry about a client library that is not the one being spoken to.
	for name := range h {
		if strings.HasPrefix(strings.ToLower(name), "x-stainless-") {
			delete(h, name)
		}
	}
}

// anthropicErrorFields reads an Anthropic error envelope. An unreadable body
// still yields something a client can print, because losing the upstream's own
// words is the failure this gateway exists to avoid.
func anthropicErrorFields(body []byte) (kind, message string) {
	var env struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Error.Message != "" {
		return env.Error.Type, env.Error.Message
	}
	if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
		return "", trimmed
	}
	return "", "the upstream refused the request without saying why"
}
