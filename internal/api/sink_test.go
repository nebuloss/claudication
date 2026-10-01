package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeReshaper records which of the three shapes the sink chose and produces
// recognisable output for each, so a test can tell them apart in the body.
type fakeReshaper struct {
	stream      *fakeStream
	completed   []byte
	completeErr error
	refused     []byte
	refusedAt   int
	failures    []string
}

type fakeStream struct {
	out      FlushWriter
	finished bool
	cause    error
}

func (f *fakeStream) Write(p []byte) (int, error) {
	n, err := fmt.Fprintf(f.out, "<%s>", p)
	f.out.Flush()
	return n, err
}

func (f *fakeStream) Finish(cause error) { f.finished, f.cause = true, cause }

func (r *fakeReshaper) Stream(out FlushWriter) StreamWriter {
	r.stream = &fakeStream{out: out}
	return r.stream
}

func (r *fakeReshaper) Complete(body []byte) ([]byte, error) {
	r.completed = append([]byte(nil), body...)
	if r.completeErr != nil {
		return nil, r.completeErr
	}
	return []byte("converted:" + string(body)), nil
}

func (r *fakeReshaper) Refusal(status int, body []byte) []byte {
	r.refusedAt, r.refused = status, append([]byte(nil), body...)
	return []byte("refusal:" + string(body))
}

func (r *fakeReshaper) Failure(kind, message string) []byte {
	r.failures = append(r.failures, kind+": "+message)
	return []byte("failure:" + kind)
}

// flushRecorder counts flushes, because a frame that is written but not
// flushed is the idle gap that fails a client's turn.
type flushRecorder struct {
	*httptest.ResponseRecorder
	flushes int
}

func (f *flushRecorder) Flush() { f.flushes++; f.ResponseRecorder.Flush() }

// A streamed answer must reach the client frame by frame, flushed, with the
// headers that stop a proxy buffering it — and must end in the dialect's
// terminal event however the relay stopped.
func TestReshapingSinkStreams(t *testing.T) {
	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	r := &fakeReshaper{}
	s := NewReshapingSink(rec, r)
	s.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	s.Header().Set("Content-Length", "10")
	s.Header().Set("Content-Encoding", "gzip")
	s.Header().Set("Request-Id", "req_1")
	s.WriteHeader(http.StatusOK)
	s.WriteHeader(http.StatusTeapot) // a second header is ignored, as net/http does
	_, _ = s.Write([]byte("a"))
	_, _ = s.Write([]byte("b"))
	cause := errors.New("cut")
	s.Close(cause)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}
	if got := rec.Body.String(); got != "<a><b>" {
		t.Errorf("body = %q, want each write through the stream writer", got)
	}
	if rec.flushes < 2 {
		t.Errorf("flushed %d times for 2 frames", rec.flushes)
	}
	h := rec.Header()
	if h.Get("Content-Type") != "text/event-stream" || h.Get("Cache-Control") != "no-cache" || h.Get("X-Accel-Buffering") != "no" {
		t.Errorf("headers = %v, want SSE with buffering switched off", h)
	}
	if h.Get("Content-Length") != "" || h.Get("Content-Encoding") != "" {
		t.Errorf("headers = %v: length and encoding describe a body no longer being sent", h)
	}
	if h.Get("Request-Id") != "req_1" {
		t.Error("an unrelated upstream header was dropped")
	}
	if !r.stream.finished || r.stream.cause != cause {
		t.Errorf("Finish called = %v with %v, want the relay's error", r.stream.finished, r.stream.cause)
	}
	if s.Unwrap() != rec {
		t.Error("Unwrap must reach the real writer so ResponseController can flush it")
	}
}

// A non-streaming answer is held until it is whole, converted once, and sent
// with a status and headers that describe the converted body.
func TestReshapingSinkConvertsAWholeAnswer(t *testing.T) {
	rec := httptest.NewRecorder()
	r := &fakeReshaper{}
	s := NewReshapingSink(rec, r)
	s.Header().Set("Content-Type", "application/json")
	s.Header().Set("Content-Length", "7")
	s.Header().Set("Anthropic-Ratelimit-Requests-Remaining", "9")
	_, _ = s.Write([]byte(`{"a":`)) // implicit 200, as net/http does
	_, _ = s.Write([]byte(`1}`))
	if rec.Body.Len() != 0 || len(rec.Header()) != 0 {
		t.Fatalf("wrote %q %v before the answer was whole", rec.Body, rec.Header())
	}
	s.Close(nil)
	if string(r.completed) != `{"a":1}` {
		t.Errorf("Complete got %q, want the whole body", r.completed)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != `converted:{"a":1}` {
		t.Errorf("answer = %d %q", rec.Code, rec.Body)
	}
	if rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("Content-Length") != "" {
		t.Errorf("headers = %v", rec.Header())
	}
	if rec.Header().Get("Anthropic-Ratelimit-Requests-Remaining") != "9" {
		t.Error("upstream headers were not forwarded")
	}
	if r.stream != nil {
		t.Error("a JSON answer was treated as a stream")
	}
}

// A streaming request that fails before the stream begins comes back as
// JSON with status 200 or as an error status; either way it is not SSE.
func TestReshapingSinkOnlyStreamsA200EventStream(t *testing.T) {
	rec := httptest.NewRecorder()
	r := &fakeReshaper{}
	s := NewReshapingSink(rec, r)
	s.Header().Set("Content-Type", "text/event-stream")
	s.Header().Set("Retry-After", "12")
	s.WriteHeader(http.StatusTooManyRequests)
	_, _ = s.Write([]byte(`{"error":{}}`))
	s.Close(nil)
	if r.stream != nil {
		t.Fatal("an error status was streamed")
	}
	// 429 and Retry-After are what a client backs off on; translating only
	// the envelope keeps them.
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "12" {
		t.Errorf("status %d Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if r.refusedAt != http.StatusTooManyRequests || rec.Body.String() != `refusal:{"error":{}}` {
		t.Errorf("refusal(%d) wrote %q", r.refusedAt, rec.Body)
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want the envelope's, not the upstream's", rec.Header().Get("Content-Type"))
	}
}

// An answer the dialect cannot convert is still an answer in the dialect —
// a client can only act on an error it can parse.
func TestReshapingSinkReportsAnUnconvertibleAnswer(t *testing.T) {
	rec := httptest.NewRecorder()
	r := &fakeReshaper{completeErr: errors.New("bad json")}
	s := NewReshapingSink(rec, r)
	s.WriteHeader(http.StatusOK)
	_, _ = s.Write([]byte(`<html>`))
	s.Close(nil)
	if rec.Code != http.StatusBadGateway || rec.Body.String() != "failure:server_error" {
		t.Errorf("answer = %d %q", rec.Code, rec.Body)
	}
	if len(r.failures) != 1 || !strings.Contains(r.failures[0], "bad json") {
		t.Errorf("failures = %v, want the conversion error named", r.failures)
	}
}

// The cap exists so a misbehaving upstream cannot grow the heap without limit;
// past it, the fragment held must never be parsed as if it were the answer.
func TestReshapingSinkCapsTheBufferedAnswer(t *testing.T) {
	rec := httptest.NewRecorder()
	r := &fakeReshaper{}
	s := NewReshapingSink(rec, r)
	_, _ = s.Write([]byte(`{"partial":`))
	n, err := s.Write(make([]byte, maxBufferedAnswer))
	if err != nil || n != maxBufferedAnswer {
		t.Errorf("Write = %d, %v: the relay must not see an error and abort mid-copy", n, err)
	}
	_, _ = s.Write([]byte(`}`))
	s.Close(nil)
	if r.completed != nil {
		t.Error("a truncated answer was handed to Complete")
	}
	if rec.Code != http.StatusBadGateway || rec.Body.String() != "failure:server_error" {
		t.Errorf("answer = %d %q", rec.Code, rec.Body)
	}
	if len(r.failures) != 1 || !strings.Contains(r.failures[0], "too large") {
		t.Errorf("failures = %v", r.failures)
	}
}

// When the relay never answered, the handler writes the error because only it
// knows why; the sink writing anything first would make that impossible.
func TestReshapingSinkWritesNothingWhenNothingCame(t *testing.T) {
	rec := httptest.NewRecorder()
	r := &fakeReshaper{}
	NewReshapingSink(rec, r).Close(errors.New("no accounts"))
	if rec.Body.Len() != 0 || len(rec.Header()) != 0 || len(r.failures) != 0 {
		t.Errorf("wrote %q %v", rec.Body, rec.Header())
	}
}

// The identity dialect's contract is that nothing changes: every byte and
// header the upstream sends reaches the client.
func TestPassthroughChangesNothing(t *testing.T) {
	rec := httptest.NewRecorder()
	s := Passthrough(rec)
	s.Header().Set("X-Up", "1")
	s.WriteHeader(http.StatusAccepted)
	_, _ = io.WriteString(s, "body")
	s.Close(errors.New("ignored"))
	if rec.Code != http.StatusAccepted || rec.Body.String() != "body" || rec.Header().Get("X-Up") != "1" {
		t.Errorf("got %d %q %v", rec.Code, rec.Body, rec.Header())
	}
	if s.Unwrap() != rec {
		t.Error("Unwrap must return the client's writer")
	}
}

// Flushed must flush through wrappers, which is what Unwrap is for: a sink
// that hid the connection from ResponseController would buffer every frame.
func TestFlushedReachesTheConnectionThroughASink(t *testing.T) {
	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	w := Flushed(Passthrough(rec))
	if _, err := io.WriteString(w, "frame"); err != nil {
		t.Fatal(err)
	}
	w.Flush()
	if rec.flushes != 1 || rec.Body.String() != "frame" {
		t.Errorf("flushes %d body %q", rec.flushes, rec.Body)
	}
}

type stubProtocol struct{ id string }

func (p stubProtocol) ID() string { return p.id }

func (stubProtocol) Title() string { return "" }

func (stubProtocol) Routes() []string { return nil }

func (stubProtocol) Decode([]byte) (Exchange, error) { return nil, nil }

func (stubProtocol) WriteError(http.ResponseWriter, int, string, string) {}

func (stubProtocol) ConversationID(http.Header, []byte) string { return "" }

// Find is how a stored switch is matched back to its surface.
func TestRegistryFind(t *testing.T) {
	r := Registry{stubProtocol{"anthropic"}, stubProtocol{"openai"}}
	if p, ok := r.Find("openai"); !ok || p.ID() != "openai" {
		t.Errorf("Find(openai) = %v, %v", p, ok)
	}
	if p, ok := r.Find("gemini"); ok || p != nil {
		t.Errorf("Find(gemini) = %v, %v, want nothing", p, ok)
	}
}
