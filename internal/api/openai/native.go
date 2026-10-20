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
// A client's model name in the model field is still the gateway's to resolve,
// and its answer still names it: the response.* events carry the model in the
// same top-level response object the translation writes, so ModelEvents is
// the same list.
func (API) DecodeNative(body []byte) (api.Exchange, error) {
	return native{body: body, prologue: request.Peek(body)}, nil
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

// Sink relays the answer as it came, saying what it is when the backend did
// not: a streamed answer comes back from it with no Content-Type at all
// (measured 2026-10-20, internal/provider/openai/testdata/live-stream.txt),
// which Go's server would then label text/plain. An OpenAI SDK reading that
// does not read it as a stream, and nothing below — the model renamer above
// all — can tell it is one. So a successful answer to a streaming request
// that names no type is named text/event-stream, which is what it is. A
// refusal is left as it came: it is JSON, and says so.
func (e native) Sink(w http.ResponseWriter) api.Sink {
	if !e.prologue.Stream {
		return api.Passthrough(w)
	}
	return api.Passthrough(&streamLabel{ResponseWriter: w})
}

// streamLabel names a streamed answer's type when its upstream did not.
type streamLabel struct {
	http.ResponseWriter
	wrote bool
}

func (s *streamLabel) WriteHeader(status int) {
	if !s.wrote {
		s.wrote = true
		ct := strings.ToLower(strings.TrimSpace(s.Header().Get("Content-Type")))
		// text/plain too: what a server between labels an untyped body.
		if status == http.StatusOK && (ct == "" || strings.HasPrefix(ct, "text/plain")) {
			s.Header().Set("Content-Type", "text/event-stream")
		}
	}
	s.ResponseWriter.WriteHeader(status)
}

func (s *streamLabel) Write(p []byte) (int, error) {
	if !s.wrote {
		s.WriteHeader(http.StatusOK)
	}
	return s.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the writer beneath, to flush it.
func (s *streamLabel) Unwrap() http.ResponseWriter { return s.ResponseWriter }
