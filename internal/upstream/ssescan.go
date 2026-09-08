package upstream

import (
	"bytes"
	"encoding/json"
)

// sseEvent names the events this scanner reacts to. An enum rather than the
// wire: comparing bytes to a constant costs nothing, where keeping the name
// meant allocating a string for every `event:` line in the stream — thousands
// per response, none of which outlive the next line.
type sseEvent uint8

const (
	evOther sseEvent = iota
	evError
	evMessageStart
	evMessageDelta
)

func eventOf(name []byte) sseEvent {
	switch {
	case bytes.Equal(name, []byte("error")):
		return evError
	case bytes.Equal(name, []byte("message_start")):
		return evMessageStart
	case bytes.Equal(name, []byte("message_delta")):
		return evMessageDelta
	}
	return evOther
}

// maxPending bounds the bytes held while waiting for a newline. SSE records are
// small, so anything past this is not a record we are going to make sense of.
const maxPending = 1 << 20

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
	// buf holds bytes not yet consumed, and start is where the next line
	// begins. Slicing rather than a bytes.Buffer is what keeps this cheap: the
	// old version read each line out with ReadBytes — an allocation per line,
	// measured at 1.4x the size of the whole stream — and then wrote any
	// partial line back into the same buffer it had just read it from, which
	// made a long line quadratic in the number of chunks it arrived in. A 1 MB
	// tool-use payload delivered in MTU-sized pieces spent ~111 ms doing that,
	// synchronously, between two writes to the client.
	buf     []byte
	start   int
	event   sseEvent
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

	// Reclaim what has been consumed. Only when a line has completed, so a
	// long line still being assembled grows by amortised append instead of
	// being recopied for every chunk that arrives.
	if s.start > 0 {
		s.buf = append(s.buf[:0], s.buf[s.start:]...)
		s.start = 0
	}
	if len(s.buf)+len(chunk) > maxPending {
		s.buf = s.buf[:0]
	}
	s.buf = append(s.buf, chunk...)

	for {
		i := bytes.IndexByte(s.buf[s.start:], '\n')
		if i < 0 {
			return // partial line; wait for the rest
		}
		line := s.buf[s.start : s.start+i]
		s.start += i + 1
		s.line(bytes.TrimSuffix(line, []byte("\r")))
	}
}

// done exists for the bodyTee interface. An SSE scanner has already recorded
// everything it is going to by the time the stream ends.
func (s *sseScanner) done() {}

func (s *sseScanner) line(line []byte) {
	switch {
	case len(line) == 0:
		s.event = evOther
	case bytes.HasPrefix(line, []byte("event:")):
		s.event = eventOf(bytes.TrimSpace(line[len("event:"):]))
	case bytes.HasPrefix(line, []byte("data:")):
		s.data(bytes.TrimSpace(line[len("data:"):]))
	}
}

func (s *sseScanner) data(payload []byte) {
	if len(payload) == 0 || payload[0] != '{' {
		return
	}

	switch s.event {
	case evError:
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

	case evMessageStart:
		var m struct {
			Message struct {
				Usage usageJSON `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(payload, &m) == nil {
			m.Message.Usage.applyTo(s.usage)
		}

	case evMessageDelta:
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
