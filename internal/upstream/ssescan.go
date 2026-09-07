package upstream

import (
	"bytes"
	"encoding/json"
)

// sseScanner watches a relayed SSE stream for token usage and mid-stream
// errors, without ever touching the bytes on their way to the client.
//
// It exists for one correctness reason beyond stats. The Messages stream can
// carry an error *after* a 200 — the documented example is overloaded_error,
// the SSE equivalent of a 529. A relay that only looks at the status code
// records that as a success, so the account is never cooled down and the
// failure is invisible. auth2api has exactly this bug.
//
// Everything here is best-effort: a malformed line is skipped rather than
// failing the request, because nothing this type learns is worth breaking a
// response over.
type sseScanner struct {
	buf     bytes.Buffer
	event   string
	usage   *Usage
	errOut  *string
	stopped bool
}

func newSSEScanner(usage *Usage, errOut *string) *sseScanner {
	return &sseScanner{usage: usage, errOut: errOut}
}

// feed consumes a chunk that has already been written to the client.
func (s *sseScanner) feed(chunk []byte) {
	if s.stopped {
		return
	}
	// Guard against a pathological line with no newline growing without
	// bound; SSE records are small, so anything huge is not a record.
	if s.buf.Len() > 1<<20 {
		s.buf.Reset()
	}
	s.buf.Write(chunk)

	for {
		line, err := s.buf.ReadBytes('\n')
		if err != nil {
			// Partial line: put it back and wait for the rest.
			s.buf.Write(line)
			return
		}
		s.line(bytes.TrimRight(line, "\r\n"))
	}
}

// done exists for the bodyTee interface. An SSE scanner has already recorded
// everything it is going to by the time the stream ends.
func (s *sseScanner) done() {}

func (s *sseScanner) line(line []byte) {
	switch {
	case len(line) == 0:
		s.event = ""
	case bytes.HasPrefix(line, []byte("event:")):
		s.event = string(bytes.TrimSpace(line[len("event:"):]))
	case bytes.HasPrefix(line, []byte("data:")):
		s.data(bytes.TrimSpace(line[len("data:"):]))
	}
}

func (s *sseScanner) data(payload []byte) {
	if len(payload) == 0 || payload[0] != '{' {
		return
	}

	switch s.event {
	case "error":
		var e struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(payload, &e) == nil && e.Error.Type != "" {
			*s.errOut = e.Error.Type + ": " + e.Error.Message
			s.stopped = true
		}

	case "message_start":
		var m struct {
			Message struct {
				Usage usageJSON `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(payload, &m) == nil {
			m.Message.Usage.applyTo(s.usage)
		}

	case "message_delta":
		// Usage on message_delta is cumulative, so assignment is correct and
		// accumulation would double count.
		var d struct {
			Usage usageJSON `json:"usage"`
		}
		if json.Unmarshal(payload, &d) == nil {
			d.Usage.applyTo(s.usage)
		}
	}
}

type usageJSON struct {
	InputTokens              *int `json:"input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
}

// applyTo copies only the fields the event actually carried, so a
// message_delta reporting output tokens does not wipe the input count that
// arrived on message_start.
func (u usageJSON) applyTo(dst *Usage) {
	if u.InputTokens != nil {
		dst.InputTokens = *u.InputTokens
	}
	if u.OutputTokens != nil {
		dst.OutputTokens = *u.OutputTokens
	}
	if u.CacheReadInputTokens != nil {
		dst.CacheReadTokens = *u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens != nil {
		dst.CacheCreationTokens = *u.CacheCreationInputTokens
	}
}
