package capture

import (
	"io"
	"net/http"
	"sync"
	"time"
)

// Exchange is one client request and everything that passed for it. While
// the request runs, its own goroutine writes it — and the relay's, reading
// an upstream answer — and once Finish has filed it nothing writes it again.
type Exchange struct {
	c *Capture

	ID      uint64
	KeyID   string
	KeyName string
	At      time.Time
	Route   string
	// Event is the request log's row for this request, when one was written.
	Event int64
	// What served it, as far as it got.
	Status   int
	Model    string
	Provider string

	// Client is the client's leg: its request as the gateway read it, and
	// the answer as the client was sent it.
	Client Leg
	// Upstream is one leg per attempt, in order.
	Upstream []*Leg

	mu       sync.Mutex
	finished bool
	bytes    int64
}

// Leg is one request and its answer, on one side of the gateway.
type Leg struct {
	// Dialect is the API the bodies are in: a client API's id (anthropic,
	// openai, gemini) for the client's leg, and for an upstream's the
	// dialect that upstream speaks.
	Dialect string
	// Account is the account an upstream attempt was made on.
	Account string
	Started time.Time
	Request Message
	// Response is nil until an answer began, and stays nil for an attempt
	// that got none.
	Response *Message
	// Error is why an attempt got no answer.
	Error string
	// Ended is when the answer was read to its end, or zero.
	Ended time.Time
}

// Message is a request or an answer: its line, its headers and its body.
type Message struct {
	Method string
	URL    string
	Proto  string
	Status int
	Header http.Header
	Body   Body
}

// Body is the bytes of one body, cut at BodyCap, with when each part
// arrived.
type Body struct {
	mu        sync.Mutex
	start     time.Time
	data      []byte
	size      int64
	truncated bool
	chunks    []Chunk
}

// Chunk is one write: when, from the exchange's start, and how many bytes.
type Chunk struct {
	AtMS  int64 `json:"at_ms"`
	Bytes int   `json:"bytes"`
}

func (b *Body) write(p []byte) {
	if len(p) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.size += int64(len(p))
	if len(b.chunks) < maxChunks {
		b.chunks = append(b.chunks, Chunk{AtMS: time.Since(b.start).Milliseconds(), Bytes: len(p)})
	}
	room := BodyCap - len(b.data)
	if room <= 0 {
		b.truncated = true
		return
	}
	if len(p) > room {
		p = p[:room]
		b.truncated = true
	}
	b.data = append(b.data, p...)
}

// Bytes is what was kept of the body.
func (b *Body) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data
}

// Size is the body's whole length, kept or not.
func (b *Body) Size() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.size
}

// Truncated is whether bytes past BodyCap were dropped.
func (b *Body) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

// Chunks is when each part arrived.
func (b *Body) Chunks() []Chunk {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.chunks
}

func (b *Body) held() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return int64(len(b.data)) + int64(len(b.chunks))*16
}

func (m *Message) held() int64 {
	if m == nil {
		return 0
	}
	n := m.Body.held() + int64(len(m.URL))
	for k, vs := range m.Header {
		n += int64(len(k))
		for _, v := range vs {
			n += int64(len(v))
		}
	}
	return n
}

func (x *Exchange) size() int64 {
	x.mu.Lock()
	defer x.mu.Unlock()
	n := x.Client.Request.held() + x.Client.Response.held()
	for _, l := range x.Upstream {
		n += l.Request.held() + l.Response.held()
	}
	return n
}

// Served notes what served the request, as far as it is known.
func (x *Exchange) Served(model, provider string) {
	if x == nil {
		return
	}
	x.mu.Lock()
	x.Model, x.Provider = model, provider
	x.mu.Unlock()
}

// Logged ties the exchange to its request log row.
func (x *Exchange) Logged(event int64) {
	if x == nil || event == 0 {
		return
	}
	x.mu.Lock()
	x.Event = event
	x.mu.Unlock()
}

// Finish files the exchange. Called once the client's answer is done; a
// second call does nothing.
func (x *Exchange) Finish() {
	if x == nil {
		return
	}
	x.mu.Lock()
	if x.finished {
		x.mu.Unlock()
		return
	}
	x.finished = true
	if r := x.Client.Response; r != nil {
		x.Status = r.Status
		x.Client.Ended = time.Now()
	}
	x.mu.Unlock()
	x.c.commit(x)
}

// Writer wraps the writer the client is answered through, recording the
// answer as it goes. It keeps Unwrap, so flushing still reaches the
// connection.
func (x *Exchange) Writer(w http.ResponseWriter) http.ResponseWriter {
	if x == nil {
		return w
	}
	return &clientWriter{ResponseWriter: w, x: x}
}

type clientWriter struct {
	http.ResponseWriter
	x *Exchange
}

func (w *clientWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *clientWriter) answer(status int) *Message {
	x := w.x
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.Client.Response == nil {
		x.Client.Response = &Message{
			Proto:  x.Client.Request.Proto,
			Status: status,
			Header: redact(w.ResponseWriter.Header()),
		}
		x.Client.Response.Body.start = x.At
	}
	return x.Client.Response
}

func (w *clientWriter) WriteHeader(status int) {
	w.answer(status)
	w.ResponseWriter.WriteHeader(status)
}

func (w *clientWriter) Write(p []byte) (int, error) {
	m := w.answer(http.StatusOK)
	n, err := w.ResponseWriter.Write(p)
	m.Body.write(p[:n])
	return n, err
}

// The relay's side: one leg per attempt (relay.Tap).

// Sent records an attempt's request as it went upstream.
func (x *Exchange) Sent(dialect, account string, req *http.Request, body []byte) {
	if x == nil {
		return
	}
	l := &Leg{
		Dialect: dialect,
		Account: account,
		Started: time.Now(),
		Request: Message{
			Method: req.Method,
			URL:    redactURL(req.URL.String()),
			Proto:  "HTTP/1.1",
			Header: redact(req.Header),
		},
	}
	l.Request.Body.start = x.At
	l.Request.Body.write(body)
	x.mu.Lock()
	x.Upstream = append(x.Upstream, l)
	x.mu.Unlock()
}

func (x *Exchange) current() *Leg {
	x.mu.Lock()
	defer x.mu.Unlock()
	if len(x.Upstream) == 0 {
		return nil
	}
	return x.Upstream[len(x.Upstream)-1]
}

// Failed records why the current attempt got no answer.
func (x *Exchange) Failed(err error) {
	if x == nil || err == nil {
		return
	}
	if l := x.current(); l != nil {
		x.mu.Lock()
		l.Error = err.Error()
		l.Ended = time.Now()
		x.mu.Unlock()
	}
}

// Answered records the current attempt's answer, returning the body to read
// in its place: every byte read through it is recorded.
func (x *Exchange) Answered(status int, header http.Header, body io.ReadCloser) io.ReadCloser {
	if x == nil {
		return body
	}
	l := x.current()
	if l == nil {
		return body
	}
	m := &Message{Proto: "HTTP/1.1", Status: status, Header: redact(header)}
	m.Body.start = x.At
	x.mu.Lock()
	l.Response = m
	x.mu.Unlock()
	return &teeBody{ReadCloser: body, m: m, x: x, l: l}
}

type teeBody struct {
	io.ReadCloser
	m    *Message
	x    *Exchange
	l    *Leg
	once sync.Once
}

func (t *teeBody) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	t.m.Body.write(p[:n])
	if err != nil {
		t.end()
	}
	return n, err
}

func (t *teeBody) Close() error {
	t.end()
	return t.ReadCloser.Close()
}

func (t *teeBody) end() {
	t.once.Do(func() {
		t.x.mu.Lock()
		t.l.Ended = time.Now()
		t.x.mu.Unlock()
	})
}
