package openai

import (
	"net/http"
	"strings"

	"claudication/internal/api"
	"claudication/internal/request"
)

// DecodeNative is the exchange for a Responses request going to an upstream
// that speaks Responses itself — ChatGPT's Codex backend — rather than being
// translated for Claude.
//
// It is the identity, as the Anthropic surface is for Messages: the caller's
// body goes up as it came, its headers with it, and the answer comes back
// byte for byte. Nothing in doc.go's mapping applies, because there is
// nothing to map: Codex wrote this request for this backend. In particular
// Headers keeps what the translating exchange strips — originator,
// session_id, x-codex-* — because there they describe a protocol that is no
// longer being spoken, and here they are the protocol. The provider adds the
// account's credential and nothing else (internal/provider/openai).
//
// An alias in the model field is still the gateway's to resolve, and its
// answer still names the alias: the response.* events carry the model in the
// same top-level response object the translation writes, so ModelEvents is
// the same list.
func (API) DecodeNative(body []byte) (api.Exchange, error) {
	return native{body: body, prologue: request.Peek(body)}, nil
}

// FallbackFor is the alias a request naming model is sent to when no
// provider's list names it: the large role, as every Codex request was
// before the gateway had a second provider — Codex's own default ids
// (gpt-5-codex and the like) are not Claude's, and a gateway with no ChatGPT
// account should still serve them. A name that already looks like Claude's is
// sent on as it is, as the translation always did: the upstream says whether
// it knows it.
func (a API) FallbackFor(model string) string {
	if strings.HasPrefix(model, claudeModelPrefix) {
		return ""
	}
	return a.model
}

type native struct {
	body     []byte
	prologue request.Prologue
}

func (e native) Request() []byte { return e.body }
func (e native) Model() string   { return e.prologue.Model }
func (e native) Streaming() bool { return e.prologue.Stream }

// Prologue is what DecodeNative read, for the relay not to read it again.
func (e native) Prologue() request.Prologue { return e.prologue }

// Headers does nothing: every header Codex sent means something to the
// backend it was written for.
func (native) Headers(http.Header) {}

func (native) Sink(w http.ResponseWriter) api.Sink { return api.Passthrough(w) }
