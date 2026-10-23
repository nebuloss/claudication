// Package anthropicwire is the shape of what the Anthropic Messages API
// answers: a message, its content blocks and usage, the streamed events and
// their names, and the error envelope.
//
// Shapes only, with no behaviour, so it sits at the bottom of the layering and
// anything may read an answer with it: the provider package that meters and
// classifies answers for the relay, and the OpenAI dialect that translates
// them, which is below the provider and so could not share its types. Each
// declared its own copy before, and copies of a wire format are where one
// reader learns a field the other never hears of.
//
// What to do with these — which events count as content, how usage is
// accumulated across a stream — stays with the reader that needs it.
package anthropicwire

import "encoding/json"

// The streamed event names, as they appear on `event:` lines and in each
// payload's type field.
const (
	EventMessageStart      = "message_start"
	EventContentBlockStart = "content_block_start"
	EventContentBlockDelta = "content_block_delta"
	EventContentBlockStop  = "content_block_stop"
	EventMessageDelta      = "message_delta"
	EventMessageStop       = "message_stop"
	EventPing              = "ping"
	EventError             = "error"
)

// Usage is the token accounting a message, a message_start and a
// message_delta each carry. InputTokens is the uncached part of the prompt;
// the cache reads and writes are reported beside it, not inside it.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// ContentBlock is one block of a message: text, thinking, or a tool call.
// Fields a given type does not carry stay zero.
type ContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Thinking string `json:"thinking"`
	// Signature is a thinking block's; Data a redacted_thinking block's.
	Signature string          `json:"signature"`
	Data      string          `json:"data"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

// Message is a complete answer, and the message a message_start opens.
type Message struct {
	ID         string         `json:"id"`
	Model      string         `json:"model"`
	StopReason string         `json:"stop_reason"`
	Content    []ContentBlock `json:"content"`
	Usage      Usage          `json:"usage"`
}

// Delta is what a content_block_delta or a message_delta adds.
type Delta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	PartialJSON string `json:"partial_json"`
	Thinking    string `json:"thinking"`
	Signature   string `json:"signature"`
	StopReason  string `json:"stop_reason"`
}

// Error is what went wrong, in the upstream's own words: Type is one of its
// error types (overloaded_error, rate_limit_error, …).
type Error struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ErrorEnvelope is an error answer, and the payload of an error event.
type ErrorEnvelope struct {
	Type  string `json:"type"`
	Error Error  `json:"error"`
}
