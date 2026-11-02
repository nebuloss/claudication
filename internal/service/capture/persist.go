package capture

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"claudication/internal/secret"
	"claudication/internal/store/recordings"
)

// Keeping recordings on disk (internal/store/recordings), while
// capture.retention-days asks for it.
//
// Every recording is still made and held in memory first, as without it. Once
// an exchange is finished and has its request log row, it is handed to one
// writer goroutine: never written on the request's own, and never waited for —
// the hand-off is a send that gives up when the queue is full, and the
// recording then stays in memory only and is counted as dropped. The writer
// cuts each body by its content (chunk.go), compresses and seals each chunk
// the file does not have yet, seals the rest of the exchange — every leg,
// every header, each body as its list of chunks — and stores it under its row's
// id. Nothing in it knows an API or a provider: what is stored is the bytes as
// they passed, so what is read back is exactly what was recorded.
//
// Sealed with keys derived from the instance key for this purpose alone, so a
// recording can never be opened as a credential: a stolen file is noise
// without secret.key, as the database is.

const (
	// queueBytes bounds what waits for the writer, on top of the memory
	// budget: the exchanges it keeps alive past their eviction.
	queueBytes = 64 << 20
	// freeFloor is the free space below which nothing is written, whatever
	// the budget: the disk the gateway runs on is shared with its database.
	freeFloor = 100 << 20
	// keycheck is the file's proof of which key sealed it.
	keycheck = "claudication recordings"
)

type persister struct {
	db    *recordings.DB
	seal  *secret.Sealer
	hash  func([]byte) []byte
	log   *slog.Logger
	queue chan *Exchange
	done  chan struct{}

	mu     sync.Mutex
	closed bool

	queued atomic.Int64
	drops  atomic.Int64
	stored atomic.Int64
	// lastSkip rate-limits the warning of a write skipped for want of space.
	lastSkip atomic.Int64
}

// Persist starts keeping what is recorded in db, sealed under keys derived
// from sealer. A file sealed under another key — a restored backup's, a key
// replaced — cannot be read, and is emptied.
func (c *Capture) Persist(ctx context.Context, db *recordings.DB, sealer *secret.Sealer, log *slog.Logger) error {
	seal, err := sealer.Derive("recordings")
	if err != nil {
		return err
	}
	hash, err := sealer.Hasher("recording chunks")
	if err != nil {
		return err
	}
	if v, ok, err := db.Meta(ctx, "keycheck"); err != nil {
		return err
	} else if ok {
		if got, err := seal.OpenBytes(v, []byte("keycheck")); err != nil || string(got) != keycheck {
			log.Warn("stored recordings were sealed under another key; emptying them", "file", db.Path())
			if err := db.Reset(ctx); err != nil {
				return err
			}
			ok = false
		}
		if !ok {
			if err := setKeycheck(ctx, db, seal); err != nil {
				return err
			}
		}
	} else if err := setKeycheck(ctx, db, seal); err != nil {
		return err
	}
	p := &persister{
		db: db, seal: seal, hash: hash, log: log,
		queue: make(chan *Exchange, 1024), done: make(chan struct{}),
	}
	c.mu.Lock()
	c.persist = p
	c.mu.Unlock()
	go p.run()
	return nil
}

func setKeycheck(ctx context.Context, db *recordings.DB, seal *secret.Sealer) error {
	v, err := seal.SealBytes([]byte(keycheck), []byte("keycheck"))
	if err != nil {
		return err
	}
	return db.SetMeta(ctx, "keycheck", v)
}

// Persisting is whether recordings are being kept on disk: false while they
// are kept in memory only, which is also what a file that could not be
// opened leaves, whatever the configuration asked.
func (c *Capture) Persisting() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.persist != nil
}

// Close stops taking exchanges and writes what is queued, until ctx ends.
// Called once the gateway has answered everything it accepted, so what
// finished during the drain is kept too.
func (c *Capture) Close(ctx context.Context) {
	c.mu.Lock()
	p := c.persist
	c.mu.Unlock()
	if p == nil {
		return
	}
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.queue)
	}
	p.mu.Unlock()
	select {
	case <-p.done:
	case <-ctx.Done():
		p.log.Warn("recordings still queued at shutdown were not written", "queued_bytes", p.queued.Load())
	}
}

// offer hands an exchange to the writer, or drops it when the queue is full.
func (p *persister) offer(x *Exchange) {
	size := x.bytes
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.queued.Load()+size > queueBytes {
		p.drops.Add(1)
		return
	}
	select {
	case p.queue <- x:
		p.queued.Add(size)
	default:
		p.drops.Add(1)
	}
}

func (p *persister) run() {
	defer close(p.done)
	for x := range p.queue {
		p.queued.Add(-x.bytes)
		if free, ok := recordings.Free(filepath.Dir(p.db.Path())); ok && free < freeFloor {
			p.drops.Add(1)
			if now := time.Now().Unix(); now-p.lastSkip.Load() > 60 {
				p.lastSkip.Store(now)
				p.log.Warn("not keeping recordings: the disk is nearly full", "free_bytes", free)
			}
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := p.write(ctx, x); err != nil {
			p.drops.Add(1)
			p.log.Warn("could not keep a recording", "event", x.Event, "err", err)
		} else {
			p.stored.Add(1)
		}
		cancel()
	}
}

// The stored form: the exchange without its bodies, each body its chunks.

type storedExchange struct {
	Event        int64       `json:"event"`
	KeyID        string      `json:"key_id"`
	KeyName      string      `json:"key_name,omitempty"`
	At           time.Time   `json:"at"`
	Route        string      `json:"route"`
	Status       int         `json:"status"`
	Model        string      `json:"model,omitempty"`
	Provider     string      `json:"provider,omitempty"`
	Conversation string      `json:"conversation,omitempty"`
	Client       storedLeg   `json:"client"`
	Upstream     []storedLeg `json:"upstream,omitempty"`
}

type storedLeg struct {
	Dialect  string         `json:"dialect"`
	Account  string         `json:"account,omitempty"`
	Started  time.Time      `json:"started"`
	Ended    time.Time      `json:"ended,omitzero"`
	Error    string         `json:"error,omitempty"`
	Request  storedMessage  `json:"request"`
	Response *storedMessage `json:"response,omitempty"`
}

type storedMessage struct {
	Method    string      `json:"method,omitempty"`
	URL       string      `json:"url,omitempty"`
	Proto     string      `json:"proto,omitempty"`
	Status    int         `json:"status,omitempty"`
	Header    http.Header `json:"header,omitempty"`
	Size      int64       `json:"size"`
	Truncated bool        `json:"truncated,omitempty"`
	// Timing is each part's arrival packed as varints: when, after the
	// previous, and how many bytes.
	Timing []byte `json:"timing,omitempty"`
	// Chunks are the body's, in order, by hash.
	Chunks [][]byte `json:"chunks,omitempty"`
}

// write stores one exchange.
func (p *persister) write(ctx context.Context, x *Exchange) error {
	plain := map[string][]byte{}
	var order [][]byte
	body := func(b *Body) storedMessage {
		m := storedMessage{Size: b.Size(), Truncated: b.Truncated(), Timing: packTiming(b.Chunks())}
		for _, c := range chunks(b.Bytes()) {
			h := p.hash(c)
			m.Chunks = append(m.Chunks, h)
			if _, ok := plain[string(h)]; !ok {
				plain[string(h)] = c
				order = append(order, h)
			}
		}
		return m
	}
	message := func(m *Message) storedMessage {
		s := body(&m.Body)
		s.Method, s.URL, s.Proto, s.Status, s.Header = m.Method, m.URL, m.Proto, m.Status, m.Header
		return s
	}
	leg := func(l *Leg) storedLeg {
		s := storedLeg{Dialect: l.Dialect, Account: l.Account, Started: l.Started, Ended: l.Ended, Error: l.Error,
			Request: message(&l.Request)}
		if l.Response != nil {
			r := message(l.Response)
			s.Response = &r
		}
		return s
	}
	st := storedExchange{
		Event: x.Event, KeyID: x.KeyID, KeyName: x.KeyName, At: x.At, Route: x.Route, Status: x.Status,
		Model: x.Model, Provider: x.Provider, Conversation: x.Conversation, Client: leg(&x.Client),
	}
	for _, l := range x.Upstream {
		st.Upstream = append(st.Upstream, leg(l))
	}

	missing, err := p.db.Missing(ctx, order)
	if err != nil {
		return err
	}
	sealed := make(map[string][]byte, len(missing))
	for h := range missing {
		v, err := p.seal.SealBytes(compress(plain[h]), []byte(h))
		if err != nil {
			return err
		}
		sealed[h] = v
	}
	meta, err := json.Marshal(st)
	if err != nil {
		return err
	}
	sealedMeta, err := p.seal.SealBytes(meta, eventAAD(x.Event))
	if err != nil {
		return err
	}
	var size int64
	for _, l := range append([]storedLeg{st.Client}, st.Upstream...) {
		size += l.Request.Size
		if l.Response != nil {
			size += l.Response.Size
		}
	}
	return p.db.Put(ctx, recordings.Recording{
		Event: x.Event, At: x.At, KeyID: x.KeyID, Conversation: x.Conversation,
		Meta: sealedMeta, Bytes: size, Hashes: order,
	}, sealed)
}

func eventAAD(event int64) []byte { return []byte("recording " + strconv.FormatInt(event, 10)) }

// A chunk is stored as one byte saying how, then itself: compressed when that
// makes it smaller — JSON does, an image's base64 barely, and is kept as it is.
const (
	codecStored byte = 0
	codecFlate  byte = 1
)

func compress(b []byte) []byte {
	var buf bytes.Buffer
	buf.WriteByte(codecFlate)
	w, _ := flate.NewWriter(&buf, flate.DefaultCompression)
	_, _ = w.Write(b)
	_ = w.Close()
	if buf.Len() < len(b)+1 {
		return buf.Bytes()
	}
	return append([]byte{codecStored}, b...)
}

func decompress(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, errors.New("empty chunk")
	}
	switch b[0] {
	case codecStored:
		return b[1:], nil
	case codecFlate:
		return io.ReadAll(flate.NewReader(bytes.NewReader(b[1:])))
	}
	return nil, fmt.Errorf("chunk stored by an unknown codec %d", b[0])
}

func packTiming(cs []Chunk) []byte {
	var out []byte
	var prev int64
	for _, c := range cs {
		out = binary.AppendUvarint(out, uint64(max(0, c.AtMS-prev)))
		out = binary.AppendUvarint(out, uint64(c.Bytes))
		prev = c.AtMS
	}
	return out
}

func unpackTiming(b []byte) []Chunk {
	var out []Chunk
	var at int64
	for len(b) > 0 {
		d, n := binary.Uvarint(b)
		if n <= 0 {
			break
		}
		b = b[n:]
		size, m := binary.Uvarint(b)
		if m <= 0 {
			break
		}
		b = b[m:]
		at += int64(d)
		out = append(out, Chunk{AtMS: at, Bytes: int(size)})
	}
	return out
}

// load reads a stored exchange back, bodies and all.
func (p *persister) load(ctx context.Context, event int64) (*Exchange, error) {
	r, err := p.db.Get(ctx, event)
	if err != nil {
		return nil, err
	}
	meta, err := p.seal.OpenBytes(r.Meta, eventAAD(event))
	if err != nil {
		return nil, err
	}
	var st storedExchange
	if err := json.Unmarshal(meta, &st); err != nil {
		return nil, fmt.Errorf("recording %d: %w", event, err)
	}
	var hashes [][]byte
	seen := map[string]bool{}
	for _, l := range append([]storedLeg{st.Client}, st.Upstream...) {
		for _, m := range []*storedMessage{&l.Request, l.Response} {
			if m == nil {
				continue
			}
			for _, h := range m.Chunks {
				if !seen[string(h)] {
					seen[string(h)] = true
					hashes = append(hashes, h)
				}
			}
		}
	}
	sealed, err := p.db.Chunks(ctx, hashes)
	if err != nil {
		return nil, err
	}
	plain := make(map[string][]byte, len(sealed))
	for h, v := range sealed {
		b, err := p.seal.OpenBytes(v, []byte(h))
		if err != nil {
			return nil, fmt.Errorf("recording %d: %w", event, err)
		}
		if plain[h], err = decompress(b); err != nil {
			return nil, fmt.Errorf("recording %d: %w", event, err)
		}
	}
	// Filled in place: a body holds a lock, and is never copied.
	fill := func(dst *Message, m *storedMessage) error {
		dst.Method, dst.URL, dst.Proto, dst.Status, dst.Header = m.Method, m.URL, m.Proto, m.Status, m.Header
		dst.Body.size, dst.Body.truncated, dst.Body.chunks = m.Size, m.Truncated, unpackTiming(m.Timing)
		for _, h := range m.Chunks {
			c, ok := plain[string(h)]
			if !ok {
				return fmt.Errorf("recording %d is missing a chunk", event)
			}
			dst.Body.data = append(dst.Body.data, c...)
		}
		return nil
	}
	fillLeg := func(dst *Leg, l storedLeg) error {
		dst.Dialect, dst.Account, dst.Started, dst.Ended, dst.Error = l.Dialect, l.Account, l.Started, l.Ended, l.Error
		if err := fill(&dst.Request, &l.Request); err != nil {
			return err
		}
		if l.Response != nil {
			dst.Response = &Message{}
			return fill(dst.Response, l.Response)
		}
		return nil
	}
	x := &Exchange{
		ID: uint64(st.Event), KeyID: st.KeyID, KeyName: st.KeyName, At: st.At, Route: st.Route, Event: st.Event,
		Status: st.Status, Model: st.Model, Provider: st.Provider, Conversation: st.Conversation,
		finished: true, stored: true,
	}
	if err := fillLeg(&x.Client, st.Client); err != nil {
		return nil, err
	}
	for _, l := range st.Upstream {
		u := &Leg{}
		if err := fillLeg(u, l); err != nil {
			return nil, err
		}
		x.Upstream = append(x.Upstream, u)
	}
	x.bytes = x.size()
	return x, nil
}
