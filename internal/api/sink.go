package api

import (
	"bytes"
	"io"
	"net/http"
	"strings"
)

// Passthrough returns a Sink that hands everything to the client unchanged. It
// is the Sink for any dialect that is already Anthropic-shaped.
func Passthrough(w http.ResponseWriter) Sink {
	return passthroughSink{ResponseWriter: w}
}

type passthroughSink struct {
	http.ResponseWriter
}

func (p passthroughSink) Unwrap() http.ResponseWriter { return p.ResponseWriter }
func (passthroughSink) Close(error)                   {}

// FlushWriter is an io.Writer whose bytes can be pushed to the client at once.
//
// Every translating dialect needs one, and needs to use it after every frame:
// a client watching for an idle gap cannot tell a gateway that is thinking
// from one that has stopped, so bytes held in a buffer fail turns.
type FlushWriter interface {
	io.Writer
	Flush()
}

// Flushed wraps w so writes can be pushed through to the client immediately.
func Flushed(w http.ResponseWriter) FlushWriter {
	return flushWriter{w: w, rc: http.NewResponseController(w)}
}

type flushWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func (f flushWriter) Write(p []byte) (int, error) { return f.w.Write(p) }
func (f flushWriter) Flush()                      { _ = f.rc.Flush() }

// StreamWriter consumes the upstream's SSE bytes and writes the dialect's own.
type StreamWriter interface {
	io.Writer
	// Finish ends the answer, whatever state the source left it in. cause is
	// the error that stopped it, or nil for a clean end. A dialect whose
	// stream must end in a terminal event sends it here.
	Finish(cause error)
}

// Reshaper is what a translating dialect supplies so NewReshapingSink can turn
// the upstream's Anthropic answer into the caller's own.
//
// Three methods because an answer arrives in exactly three shapes, and which
// one is not a function of what the caller asked for: a request with
// "stream": true that fails before the stream starts comes back as plain JSON.
type Reshaper interface {
	// Stream begins a streaming answer. The returned writer receives the
	// upstream's SSE bytes as they arrive and writes to out.
	Stream(out FlushWriter) StreamWriter

	// Complete converts one whole non-streaming answer.
	Complete(body []byte) ([]byte, error)

	// Refusal converts an upstream error body. The status is forwarded as it
	// stands, so this returns only the envelope.
	Refusal(status int, body []byte) []byte

	// Failure is this dialect's envelope for a failure of the gateway's own —
	// an answer it could not read or could not fit in memory.
	Failure(kind, message string) []byte
}

// maxBufferedAnswer bounds a non-streaming answer held in memory before it is
// converted. Nothing on the normal path comes near it; it is here so a
// misbehaving upstream cannot grow the heap without limit on a path nobody is
// watching.
const maxBufferedAnswer = 32 << 20

// NewReshapingSink returns a Sink that classifies the upstream answer and
// hands each shape to r.
//
// This is the fiddly half of writing a dialect, and it is the same for all of
// them, so it lives here rather than being reimplemented per surface:
//
//   - 200 with an SSE body — the normal path. Bytes go through the dialect's
//     StreamWriter as they arrive, so one upstream frame becomes one flushed
//     frame and no idle gap opens.
//   - 200 with a JSON body — a non-streaming request, or a streaming one that
//     failed before the stream began. Buffered and converted whole.
//   - anything else — an upstream refusal. The status carries meaning the
//     client acts on (429 and its Retry-After above all), so it is forwarded
//     unchanged and only the envelope is translated.
//
// The header the upstream sent is held rather than forwarded until the shape
// is known, because the body is about to change and neither a status nor a
// Content-Length can be taken back once written.
func NewReshapingSink(w http.ResponseWriter, r Reshaper) Sink {
	return &reshapingSink{client: w, reshaper: r, header: http.Header{}}
}

type reshapingSink struct {
	client   http.ResponseWriter
	reshaper Reshaper
	header   http.Header

	status  int
	started bool
	stream  StreamWriter
	buf     bytes.Buffer
	// overflowed records that the buffered answer grew past the cap, so what
	// is in buf is a fragment and must not be parsed as if it were whole.
	overflowed bool
}

func (s *reshapingSink) Header() http.Header         { return s.header }
func (s *reshapingSink) Unwrap() http.ResponseWriter { return s.client }

func (s *reshapingSink) WriteHeader(status int) {
	if s.started {
		return
	}
	s.started = true
	s.status = status

	sse := status == http.StatusOK &&
		strings.Contains(s.header.Get("Content-Type"), "text/event-stream")
	if !sse {
		// Held back until Close, when the body has been read and the shape is
		// known.
		return
	}

	s.copyHeaders()
	s.client.Header().Set("Content-Type", "text/event-stream")
	// A proxy that buffers an SSE response produces exactly the idle gap that
	// fails a turn, and nginx in particular needs telling.
	s.client.Header().Set("Cache-Control", "no-cache")
	s.client.Header().Set("X-Accel-Buffering", "no")
	s.client.WriteHeader(http.StatusOK)
	s.stream = s.reshaper.Stream(Flushed(s.client))
}

func (s *reshapingSink) Write(p []byte) (int, error) {
	if !s.started {
		s.WriteHeader(http.StatusOK)
	}
	if s.stream != nil {
		return s.stream.Write(p)
	}
	if s.buf.Len()+len(p) > maxBufferedAnswer {
		s.overflowed = true
		return len(p), nil
	}
	return s.buf.Write(p)
}

// Close writes whatever the shape of the answer still owes the client.
func (s *reshapingSink) Close(cause error) {
	if s.stream != nil {
		// Always a terminal event, however the stream ended: the alternative
		// is a client reporting that the connection closed, which says nothing
		// about what actually happened.
		s.stream.Finish(cause)
		return
	}
	if !s.started {
		// The relay never answered. The handler writes the error, because only
		// it knows why there was none.
		return
	}

	switch {
	case s.overflowed:
		s.write(http.StatusBadGateway,
			s.reshaper.Failure("server_error", "the upstream answer was too large to translate"))

	case s.status == http.StatusOK:
		out, err := s.reshaper.Complete(s.buf.Bytes())
		if err != nil {
			s.write(http.StatusBadGateway,
				s.reshaper.Failure("server_error", "could not read the upstream answer: "+err.Error()))
			return
		}
		s.copyHeaders()
		s.write(http.StatusOK, out)

	default:
		s.copyHeaders()
		s.write(s.status, s.reshaper.Refusal(s.status, s.buf.Bytes()))
	}
}

func (s *reshapingSink) write(status int, body []byte) {
	s.client.Header().Set("Content-Type", "application/json")
	s.client.WriteHeader(status)
	_, _ = s.client.Write(body)
}

// copyHeaders forwards the upstream's headers, minus the ones that describe a
// body we are about to replace with a different one.
func (s *reshapingSink) copyHeaders() {
	for name, values := range s.header {
		switch strings.ToLower(name) {
		case "content-length", "content-encoding", "content-type":
			// The body changes shape and usually length; these would describe
			// the one that is no longer being sent.
			continue
		}
		s.client.Header()[name] = append([]string(nil), values...)
	}
}
