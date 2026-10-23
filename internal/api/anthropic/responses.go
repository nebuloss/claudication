package anthropic

import (
	"fmt"
	"net/http"
	"strings"

	"claudication/internal/api"
)

// The dialect ChatGPT's Codex backend speaks: internal/api/openai's ID,
// spelled here because this package may not import that one.
const responsesDialect = "openai"

// countTokensRoute is the route that asks for a count rather than an answer.
const countTokensRoute = "/v1/messages/count_tokens"

// Translate rebuilds a Messages request for an upstream that speaks another
// dialect: ChatGPT's Codex backend (doc.go), or Code Assist (togemini.go).
func (a API) Translate(body []byte, to api.Target) (api.Exchange, error) {
	count := to.Route == countTokensRoute
	o := options{conversation: to.Conversation, reasoning: a.reasoning, count: count}
	switch to.Dialect {
	case responsesDialect:
		tr, err := translateMessages(body, o)
		if err != nil {
			return nil, err
		}
		if count {
			return countExchange{tr: tr, note: "the ChatGPT backend counts no tokens"}, nil
		}
		return &responsesExchange{tr: tr, up: fromResponses}, nil
	case geminiDialect:
		tr, err := translateGemini(body, o)
		if err != nil {
			return nil, err
		}
		if count {
			// Code Assist has a countTokens, but its body names the model
			// where a client's model name cannot be mapped onto it (inside
			// the request, as models/<id>), so it is estimated here too.
			return countExchange{tr: tr, note: "Gemini is not asked to count"}, nil
		}
		return &responsesExchange{tr: tr, up: fromGemini}, nil
	}
	return nil, fmt.Errorf("the Anthropic Messages API cannot be translated for a %q upstream", to.Dialect)
}

// responsesExchange is one Messages request in flight to a translating
// upstream: the ChatGPT backend, or Code Assist. up reads its answer.
type responsesExchange struct {
	tr Translation
	up upstreamAnswer
}

// Request hands the body over once and lets go of it: the exchange lives as
// long as the stream, and the answer side needs only the tool names.
func (e *responsesExchange) Request() []byte {
	body := e.tr.Body
	e.tr.Body = nil
	return body
}

func (e *responsesExchange) Model() string   { return e.tr.Model }
func (e *responsesExchange) Streaming() bool { return e.tr.Stream }

// Headers drops the ones that describe the Anthropic protocol, which is not
// the one being spoken upstream: anthropic-version and anthropic-beta, the
// Stainless SDK's telemetry, Claude Code's session and app tags. Measured on
// Claude Code 2.1.304 (capture, 2026-10-20). The User-Agent stays: it says
// truly what made the request, and the backend was measured not to gate on
// who that is. The conversation is told to the backend in the body, as
// prompt_cache_key; the pool keeps it on its account by the same id.
//
// The body is JSON and the answer a stream, whatever the caller accepts:
// both backends are asked for a stream (sink.go).
func (e *responsesExchange) Headers(h http.Header) {
	for name := range h {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "anthropic-") || strings.HasPrefix(lower, "x-stainless-") {
			delete(h, name)
		}
	}
	h.Del("X-App")
	h.Del("X-Claude-Code-Session-Id")
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "text/event-stream")
}

func (e *responsesExchange) Sink(w http.ResponseWriter) api.Sink {
	return newResponsesSink(w, e.tr, e.up)
}
