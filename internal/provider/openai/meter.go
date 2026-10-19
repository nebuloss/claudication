package openai

import (
	"bytes"
	"encoding/json"
	"time"

	"claudication/internal/provider"
)

// usageJSON is the Responses usage object, as the backend sent it on
// 2026-10-19 (testdata/baseline.json):
//
//	"usage": {"input_tokens": 23,
//	          "input_tokens_details": {"cached_tokens": 0, "cache_write_tokens": 0},
//	          "output_tokens": 5,
//	          "output_tokens_details": {"reasoning_tokens": 0},
//	          "total_tokens": 28, "attribution": {...}}
type usageJSON struct {
	InputTokens        *int `json:"input_tokens"`
	OutputTokens       *int `json:"output_tokens"`
	InputTokensDetails struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

// applyTo normalises the figures into the gateway's vocabulary, which is
// Anthropic's: input excludes what was read from the cache.
//
//	input_tokens − cached_tokens   → InputTokens
//	cached_tokens                  → CacheReadTokens
//	cache_write_tokens             → CacheCreationTokens
//	output_tokens                  → OutputTokens (reasoning included)
//	reasoning_tokens               → ReasoningTokens (shown, not added)
//
// OpenAI counts cached input inside input_tokens; Anthropic does not, and
// every budget and report adds the four together. Left as it came, a cached
// turn would be counted twice. Whether cache_write_tokens is also inside
// input_tokens is not measured — every turn measured wrote nothing — so it is
// not subtracted: overcounting a write is the cheaper error for a budget.
func (u usageJSON) applyTo(dst *provider.Usage) {
	if u.InputTokens == nil && u.OutputTokens == nil {
		return
	}
	cached := u.InputTokensDetails.CachedTokens
	if u.InputTokens != nil {
		dst.InputTokens = max(*u.InputTokens-cached, 0)
	}
	if u.OutputTokens != nil {
		dst.OutputTokens = *u.OutputTokens
	}
	dst.CacheReadTokens = cached
	dst.CacheCreationTokens = u.InputTokensDetails.CacheWriteTokens
	dst.ReasoningTokens = u.OutputTokensDetails.ReasoningTokens
}

// maxPending bounds the bytes held while waiting for a newline.
//
// A line can be far longer: response.completed repeats the whole response,
// every output item included, on one data line, and a turn that wrote a large
// file makes it megabytes. Such a line is not held. It is read as it passes
// for the one thing wanted from it, the usage object, with only that object
// kept (see cut), so a long answer is recorded with its tokens rather than as
// none, and the meter's memory does not grow with the answer.
const maxPending = 1 << 20

// maxCapture bounds what is kept of a cut line from its usage key on. The
// usage object is a few hundred bytes, its attribution block a few
// kilobytes per item; this is slack.
const maxCapture = 256 << 10

// usageKey starts the usage object in a terminal event.
var usageKey = []byte(`"usage":{`)

// sseScanner reads a relayed Responses stream for its usage and for a failure
// reported inside it, without touching the bytes on their way to the client.
//
// The terminal events carry the response object: response.completed with its
// usage, response.incomplete with a reason, response.failed with an error.
// A stream that fails after its 200 is a failure, which the status alone
// would record as a success — the same reason Anthropic's meter exists.
type sseScanner struct {
	buf          []byte
	event        string
	into         provider.Reading
	stopped      bool
	firstContent bool

	// A line past maxPending, being read as it passes: cut is that one is,
	// wanted that it is a terminal event's data whose usage is worth
	// finding, capture the bytes from its last usage key on, carry the tail
	// of the last chunk, for a key split across two.
	cut     bool
	wanted  bool
	capture []byte
	carry   []byte
}

func newSSEScanner(into provider.Reading) *sseScanner {
	return &sseScanner{into: into}
}

// Feed implements provider.Meter.
//
// Only a partial line is ever copied: a chunk's complete lines are read where
// they are, so a line is copied at most once however many chunks it came in.
func (s *sseScanner) Feed(chunk []byte) {
	for len(chunk) > 0 && !s.stopped {
		i := bytes.IndexByte(chunk, '\n')
		if s.cut {
			if i < 0 {
				s.cutFeed(chunk)
				return
			}
			s.cutFeed(chunk[:i])
			s.cutDone()
			chunk = chunk[i+1:]
			continue
		}
		if i < 0 {
			if len(s.buf)+len(chunk) > maxPending {
				s.startCut(chunk)
				return
			}
			s.buf = append(s.buf, chunk...)
			return
		}
		line := chunk[:i]
		if len(s.buf) > 0 {
			s.buf = append(s.buf, line...)
			line = s.buf
		}
		s.line(bytes.TrimSuffix(line, []byte("\r")))
		s.buf = s.buf[:0]
		chunk = chunk[i+1:]
	}
}

// startCut stops holding a line that has grown past maxPending and reads the
// rest of it as it passes.
func (s *sseScanner) startCut(chunk []byte) {
	pending := append(s.buf, chunk...)
	s.cut = true
	s.wanted = s.event != "" && bytes.HasPrefix(pending, []byte("data:"))
	s.capture, s.carry = nil, nil
	// The held bytes go now; nothing of them is kept but what cutFeed finds.
	s.buf = nil
	s.cutFeed(pending)
}

// cutFeed reads part of a cut line, keeping the bytes from the last usage key
// it has seen on, up to maxCapture.
func (s *sseScanner) cutFeed(b []byte) {
	if !s.wanted {
		return
	}
	data := append(s.carry, b...)
	if i := bytes.LastIndex(data, usageKey); i >= 0 {
		s.capture = append(s.capture[:0], data[i+len(usageKey)-1:]...)
	} else if s.capture != nil {
		s.capture = append(s.capture, b...)
	}
	if len(s.capture) > maxCapture {
		s.capture = s.capture[:maxCapture]
	}
	keep := min(len(usageKey)-1, len(data))
	s.carry = append(s.carry[:0], data[len(data)-keep:]...)
}

// cutDone ends a cut line: its usage, read from what was kept, and — for a
// failure — the failure, without the message that was not kept.
func (s *sseScanner) cutDone() {
	if s.wanted && s.capture != nil && s.into.Usage != nil {
		var u usageJSON
		if json.NewDecoder(bytes.NewReader(s.capture)).Decode(&u) == nil {
			u.applyTo(s.into.Usage)
		}
	}
	switch {
	case !s.wanted:
	case s.event == evFailed, s.event == evError:
		s.fail("response_failed", "the response failed; its message was too long to keep")
	case s.event == evIncomplete:
		s.fail("incomplete", "the response ended early")
	}
	s.cut, s.wanted, s.capture, s.carry = false, false, nil, nil
}

// Done implements provider.Meter.
func (s *sseScanner) Done() {}

func (s *sseScanner) line(line []byte) {
	switch {
	case len(line) == 0:
		s.event = ""
	case bytes.HasPrefix(line, []byte("event:")):
		name := bytes.TrimSpace(line[len("event:"):])
		if k := (Provider{}).Event(name); k == provider.EventContent || k == provider.EventSettling {
			s.markContent()
		}
		// Kept as a string only for the events whose data is read; every
		// delta's name would otherwise be an allocation per line.
		switch string(name) {
		case evCompleted, evIncomplete, evFailed, evError:
			s.event = string(name)
		default:
			s.event = ""
		}
	case bytes.HasPrefix(line, []byte("data:")):
		if s.event != "" {
			s.data(bytes.TrimSpace(line[len("data:"):]))
		}
	}
}

// markContent notes the time to first token: the first event that carries
// or finishes something, as the stall hold draws the line.
func (s *sseScanner) markContent() {
	if s.firstContent || s.into.FirstContent == nil {
		return
	}
	s.firstContent = true
	if s.into.FirstContent.IsZero() {
		*s.into.FirstContent = time.Now()
	}
}

// terminal is the response object a terminal event carries, in the fields
// read.
type terminal struct {
	Response struct {
		Usage             usageJSON `json:"usage"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"response"`
	// An `error` event carries its fields at the top level, or nested.
	Code    string `json:"code"`
	Message string `json:"message"`
	Error   *struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (s *sseScanner) data(payload []byte) {
	if len(payload) == 0 || payload[0] != '{' {
		return
	}
	var t terminal
	if json.Unmarshal(payload, &t) != nil {
		return
	}
	switch s.event {
	case evCompleted:
		if s.into.Usage != nil {
			t.Response.Usage.applyTo(s.into.Usage)
		}
	case evIncomplete:
		if s.into.Usage != nil {
			t.Response.Usage.applyTo(s.into.Usage)
		}
		// Codex fails the turn on it ("Incomplete response returned,
		// reason: …"), so it is a failure here too.
		reason := "unknown"
		if d := t.Response.IncompleteDetails; d != nil && d.Reason != "" {
			reason = d.Reason
		}
		s.fail("incomplete", "the response ended early: "+reason)
	case evFailed:
		if s.into.Usage != nil {
			t.Response.Usage.applyTo(s.into.Usage)
		}
		code, msg := "response_failed", "the response failed"
		if e := t.Response.Error; e != nil {
			code, msg = firstOf(e.Code, code), firstOf(e.Message, msg)
		}
		s.fail(code, msg)
	case evError:
		code, msg := t.Code, t.Message
		if e := t.Error; e != nil {
			code, msg = firstOf(e.Code, e.Type, code), firstOf(e.Message, msg)
		}
		s.fail(firstOf(code, "error"), firstOf(msg, "the stream reported an error"))
	}
}

// fail records a failure inside the stream, in the "code: message" form the
// recorder classifies (ErrorCode).
func (s *sseScanner) fail(code, msg string) {
	if s.into.StreamError != nil {
		*s.into.StreamError = code + ": " + msg
	}
	s.stopped = true
}

func firstOf(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// maxJSONUsageBytes bounds the copy kept of a non-streamed answer.
const maxJSONUsageBytes = 8 << 20

// jsonUsage reads usage out of a non-streamed answer: the response object,
// usage at its top level. Codex always streams; another Responses client may
// not.
type jsonUsage struct {
	usage *provider.Usage
	buf   []byte
}

func newJSONUsage(usage *provider.Usage) *jsonUsage { return &jsonUsage{usage: usage} }

func (j *jsonUsage) Feed(chunk []byte) {
	if len(j.buf) >= maxJSONUsageBytes {
		return
	}
	if room := maxJSONUsageBytes - len(j.buf); len(chunk) > room {
		chunk = chunk[:room]
	}
	j.buf = append(j.buf, chunk...)
}

func (j *jsonUsage) Done() {
	if len(j.buf) == 0 || j.usage == nil {
		return
	}
	var r struct {
		Usage usageJSON `json:"usage"`
	}
	if json.Unmarshal(j.buf, &r) == nil {
		r.Usage.applyTo(j.usage)
	}
}
