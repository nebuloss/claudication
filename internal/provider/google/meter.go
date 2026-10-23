package google

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"claudication/internal/geminiwire"
	"claudication/internal/provider"
)

// applyUsage normalises a turn's figures into the gateway's vocabulary, which
// is Anthropic's:
//
//	promptTokenCount + toolUsePromptTokenCount
//	  − cachedContentTokenCount             → InputTokens
//	cachedContentTokenCount                 → CacheReadTokens
//	candidatesTokenCount + thoughtsTokenCount → OutputTokens
//	thoughtsTokenCount                      → ReasoningTokens (shown, not added)
//
// Gemini counts cached input inside the prompt figure, as OpenAI does, and
// its thinking outside the candidates figure, which neither of the others
// does; gemini-cli's own total adds all four the same way
// (converter.ts fromGenerateContentResponseUsage). Nothing is reported as a
// cache write: the backend caches implicitly and says only what it read.
func applyUsage(u *geminiwire.UsageMetadata, dst *provider.Usage) {
	if u == nil || dst == nil {
		return
	}
	cached := u.CachedContentTokenCount
	dst.InputTokens = max(u.PromptTokenCount+u.ToolUsePromptTokenCount-cached, 0)
	dst.CacheReadTokens = cached
	dst.CacheCreationTokens = 0
	dst.OutputTokens = u.CandidatesTokenCount + u.ThoughtsTokenCount
	dst.ReasoningTokens = u.ThoughtsTokenCount
}

// newMeter picks the reader by the answer's shape. A streamed turn that fails
// before it starts answers in JSON, and an answer that names no type is read
// by its first byte, as ChatGPT's had to be.
func newMeter(contentType string, into provider.Reading) provider.Meter {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	switch {
	case strings.Contains(ct, "text/event-stream"):
		return &streamMeter{into: into}
	case ct == "" || strings.HasPrefix(ct, "text/plain"):
		return &sniffMeter{into: into}
	}
	return &jsonMeter{into: into}
}

// sniffMeter becomes the stream reader or the JSON reader at the answer's
// first byte that is not white space.
type sniffMeter struct {
	into  provider.Reading
	inner provider.Meter
}

func (m *sniffMeter) Feed(chunk []byte) {
	if m.inner == nil {
		trimmed := bytes.TrimLeft(chunk, " \t\r\n")
		if len(trimmed) == 0 {
			return
		}
		if trimmed[0] == '{' || trimmed[0] == '[' {
			m.inner = &jsonMeter{into: m.into}
		} else {
			m.inner = &streamMeter{into: m.into}
		}
	}
	m.inner.Feed(chunk)
}

func (m *sniffMeter) Done() {
	if m.inner != nil {
		m.inner.Done()
	}
}

// maxLine bounds one held event. Unlike a Responses stream, no event repeats
// the answer so far — each carries the text since the last — so a line this
// long is not one worth reading, and is skipped rather than held.
const maxLine = 1 << 20

// streamMeter reads a relayed Code Assist stream for its usage and for a
// failure reported inside it, without touching the bytes on their way to the
// client.
//
// The stream is `data:` lines, each a whole geminiwire.Response, with blank
// lines between; there are no event names (server.ts requestStreamingPost
// reads it the same way). Usage rides on every event and the last one's is
// the turn's. A failure after the 200 is either an error object in place of
// an answer, a prompt that was blocked, or a finish reason that is the model
// declining — any of which the status alone would record as a success.
type streamMeter struct {
	into         provider.Reading
	buf          []byte
	skipping     bool
	stopped      bool
	firstContent bool
}

// Feed implements provider.Meter.
func (s *streamMeter) Feed(chunk []byte) {
	for len(chunk) > 0 && !s.stopped {
		i := bytes.IndexByte(chunk, '\n')
		if i < 0 {
			if !s.skipping {
				if len(s.buf)+len(chunk) > maxLine {
					s.buf, s.skipping = s.buf[:0], true
				} else {
					s.buf = append(s.buf, chunk...)
				}
			}
			return
		}
		line := chunk[:i]
		chunk = chunk[i+1:]
		if s.skipping {
			s.skipping = false
			continue
		}
		if len(s.buf) > 0 {
			s.buf = append(s.buf, line...)
			line = s.buf
		}
		s.line(bytes.TrimSuffix(line, []byte("\r")))
		s.buf = s.buf[:0]
	}
}

// Done implements provider.Meter.
func (s *streamMeter) Done() {}

func (s *streamMeter) line(line []byte) {
	payload, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok {
		return
	}
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 || payload[0] != '{' {
		return
	}
	var ev event
	if json.Unmarshal(payload, &ev) != nil {
		return
	}
	if ev.Error != nil {
		s.fail(errorCode(*ev.Error), firstOf(ev.Error.Message, "the stream reported an error"))
		return
	}
	r := ev.Response
	if r == nil {
		return
	}
	if r.UsageMetadata != nil {
		applyUsage(r.UsageMetadata, s.into.Usage)
	}
	if code, msg := failure(r); code != "" {
		s.fail(code, msg)
		return
	}
	if hasContent(r) {
		s.markContent()
	}
}

// event is one streamed line: an answer, or an error in its place.
type event struct {
	Response *geminiwire.GenerateResponse `json:"response"`
	Error    *geminiwire.Status           `json:"error"`
}

// hasContent reports whether an answer carries something the model produced.
func hasContent(r *geminiwire.GenerateResponse) bool {
	for _, c := range r.Candidates {
		if c.Content != nil && len(c.Content.Parts) > 0 {
			return true
		}
	}
	return false
}

// failure is what an answer says went wrong, as a code and a message, or
// nothing. A prompt blocked before any candidate is one; a candidate that
// finished for any reason other than stopping or running out of room is the
// model declining (SAFETY, RECITATION, PROHIBITED_CONTENT, …) or failing to
// form a call (MALFORMED_FUNCTION_CALL), and gemini-cli treats those as a
// turn that did not complete.
func failure(r *geminiwire.GenerateResponse) (code, msg string) {
	if f := r.PromptFeedback; f != nil && f.BlockReason != "" && f.BlockReason != "BLOCK_REASON_UNSPECIFIED" {
		return "blocked", "the prompt was blocked: " + f.BlockReason
	}
	for _, c := range r.Candidates {
		switch c.FinishReason {
		case "", geminiwire.FinishStop, geminiwire.FinishMaxTokens, "FINISH_REASON_UNSPECIFIED":
		default:
			return strings.ToLower(c.FinishReason), "the model stopped: " + c.FinishReason
		}
	}
	return "", ""
}

// markContent notes the time to first token.
func (s *streamMeter) markContent() {
	if s.firstContent || s.into.FirstContent == nil {
		return
	}
	s.firstContent = true
	if s.into.FirstContent.IsZero() {
		*s.into.FirstContent = time.Now()
	}
}

// fail records a failure inside the stream, in the "code: message" form the
// recorder classifies (ErrorCode).
func (s *streamMeter) fail(code, msg string) {
	if s.into.StreamError != nil {
		*s.into.StreamError = code + ": " + msg
	}
	s.stopped = true
}

// maxJSONBytes bounds the copy kept of a whole answer.
const maxJSONBytes = 8 << 20

// jsonMeter reads a whole answer: one Response from generateContent, or an
// array of them, which is what streamGenerateContent answers when asked
// without ?alt=sse.
type jsonMeter struct {
	into provider.Reading
	buf  []byte
}

func (j *jsonMeter) Feed(chunk []byte) {
	if room := maxJSONBytes - len(j.buf); room > 0 {
		j.buf = append(j.buf, chunk[:min(len(chunk), room)]...)
	}
}

func (j *jsonMeter) Done() {
	trimmed := bytes.TrimLeft(j.buf, " \t\r\n")
	if len(trimmed) == 0 {
		return
	}
	var events []event
	if trimmed[0] == '[' {
		if json.Unmarshal(trimmed, &events) != nil {
			return
		}
	} else {
		var ev event
		if json.Unmarshal(trimmed, &ev) != nil {
			return
		}
		events = []event{ev}
	}
	for _, ev := range events {
		if ev.Response == nil {
			continue
		}
		if ev.Response.UsageMetadata != nil {
			applyUsage(ev.Response.UsageMetadata, j.into.Usage)
		}
		if code, msg := failure(ev.Response); code != "" && j.into.StreamError != nil {
			*j.into.StreamError = code + ": " + msg
		}
	}
}
