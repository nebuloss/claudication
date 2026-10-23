package api

import (
	"errors"
	"net/http"
)

// ErrNoTranslation is a Translator's answer for an upstream dialect it has no
// translation into. The gateway then goes through Anthropic Messages (Chain),
// which every dialect can reach.
var ErrNoTranslation = errors.New("no translation into that dialect")

// Chain is a request that reaches its upstream in two translations, through
// Anthropic Messages: client is the caller's dialect read into Messages (a
// Protocol's Decode), and upstream is that Messages request translated for
// the upstream (the Anthropic surface's Translate).
//
// It is how a dialect reaches an upstream it has no translation of its own
// into — the OpenAI surface reaching Gemini — without one being written for
// every pair. Each translation stays in its own package, next to the
// evidence for it, and the hop between them is Messages, which both already
// speak. What the hop cannot carry is lost: a field the caller's dialect and
// the upstream share but Messages lacks. A pair where that matters gets a
// translation of its own (a Translator).
//
// The answer goes back through both in turn: the upstream's sink writes
// Messages into the client's, which writes the caller's dialect.
func Chain(client, upstream Exchange) Exchange { return chain{client: client, upstream: upstream} }

type chain struct {
	client, upstream Exchange
}

func (c chain) Request() []byte { return c.upstream.Request() }
func (c chain) Model() string   { return c.upstream.Model() }

// Streaming is what the caller asked for, which decides its timeout and its
// answer's shape.
func (c chain) Streaming() bool { return c.client.Streaming() }

// Headers is each translation's adjustment in turn: the caller's dialect's
// headers dropped, then Messages' own.
func (c chain) Headers(h http.Header) {
	c.client.Headers(h)
	c.upstream.Headers(h)
}

func (c chain) Sink(w http.ResponseWriter) Sink {
	outer := c.client.Sink(w)
	return chainSink{Sink: c.upstream.Sink(outer), outer: outer}
}

// chainSink closes both sinks, inner first: the inner one's last word is
// written into the outer one, which then has its own to say.
type chainSink struct {
	Sink
	outer Sink
}

func (s chainSink) Close(cause error) {
	s.Sink.Close(cause)
	s.outer.Close(cause)
}
