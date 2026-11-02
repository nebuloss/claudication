// Package capture records what passes between a client and the gateway, and
// between the gateway and the upstream, while an operator has asked for it:
// the bytes of each request and each answer, for the admin UI to show and to
// export as HAR.
//
// Recording is per client and switched on for a while — an hour by default —
// rather than left on: the content is the most sensitive thing the gateway
// handles, the prompts and the code and whatever was pasted into them, and it
// is kept only while someone is looking.
//
// Everything is held in memory, bounded twice: each body is cut at BodyCap,
// and the exchanges together at the budget, the oldest going first. The
// database never sees content and a backup never holds it. By default a
// restart or a handover (a deploy) forgets it; with capture.retention-days
// set, each recorded request is also kept on disk once it has its request log
// row (persist.go) — sealed, in a file of its own, within a disk budget — and
// read back from there once memory has let it go. The recording switch itself
// is never kept: a new worker never records on a switch nobody set on it.
//
// One exchange is one request from a client: its own leg, as it came and as
// it was answered, and one upstream leg per attempt the relay made — a retry
// on another account is another leg, so a request that failed over shows
// both.
package capture

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"claudication/internal/live"
	"claudication/internal/store/recordings"
)

const (
	// DefaultBudget bounds every exchange held, together. An agent turn is
	// routinely a few megabytes each way; this is some dozens of them.
	DefaultBudget = 256 << 20
	// BodyCap bounds one body. Past it the bytes are counted and dropped, and
	// the body says it was cut.
	BodyCap = 8 << 20
	// maxChunks bounds the timing kept per body: one entry per write, which
	// for a stream is one per event or so.
	maxChunks = 20000
	// DefaultFor is how long recording stays on when switched on, and MaxFor
	// the longest it can be asked for.
	DefaultFor = time.Hour
	MaxFor     = 24 * time.Hour
)

// Capture is the gateway's recorder: which clients are being recorded, and
// what was recorded for them.
type Capture struct {
	mu    sync.Mutex
	until map[string]time.Time
	held  []*Exchange // in the order they finished, oldest first
	// byID finds a held exchange. Not a search over held: ids are given out
	// when a request starts and held is in the order requests finish, so two
	// that overlap are out of id order there.
	byID map[uint64]*Exchange
	// byEvent finds one by its request log row: the id a recording is
	// known by outside this process, on disk and in every link.
	byEvent map[int64]*Exchange
	// persist keeps recordings on disk, when the gateway is set to.
	persist *persister
	used    int64
	budget  int64
	next    uint64
	hub     *live.Hub
	now     func() time.Time
}

// New returns a recorder holding at most budget bytes; zero or less is the
// default.
func New(budget int64) *Capture {
	if budget <= 0 {
		budget = DefaultBudget
	}
	return &Capture{
		until: map[string]time.Time{}, byID: map[uint64]*Exchange{}, byEvent: map[int64]*Exchange{},
		budget: budget, now: time.Now,
	}
}

// Announce tells h of every change from now on.
func (c *Capture) Announce(h *live.Hub) { c.hub = h }

// Record switches recording on for a client's key for d, clamped to MaxFor,
// and returns when it ends.
func (c *Capture) Record(keyID string, d time.Duration) time.Time {
	if d <= 0 {
		d = DefaultFor
	}
	d = min(d, MaxFor)
	c.mu.Lock()
	until := c.now().Add(d)
	c.until[keyID] = until
	c.mu.Unlock()
	c.hub.Changed(live.Captures, live.Clients)
	return until
}

// Stop switches recording off for a key. What it recorded stays.
func (c *Capture) Stop(keyID string) {
	c.mu.Lock()
	delete(c.until, keyID)
	c.mu.Unlock()
	c.hub.Changed(live.Captures, live.Clients)
}

// Until is when a key's recording ends, or zero when it is not recording.
func (c *Capture) Until(keyID string) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.untilLocked(keyID)
}

func (c *Capture) untilLocked(keyID string) time.Time {
	u, ok := c.until[keyID]
	if !ok {
		return time.Time{}
	}
	if !c.now().Before(u) {
		delete(c.until, keyID)
		return time.Time{}
	}
	return u
}

// Begin starts an exchange for a request from keyID, or returns nil when the
// key is not being recorded — and every method of a nil exchange does
// nothing, so a caller need not ask. body is the request as read, after any
// content coding was opened.
func (c *Capture) Begin(keyID, keyName, route, dialect string, r *http.Request, body []byte) *Exchange {
	if c == nil || keyID == "" {
		return nil
	}
	c.mu.Lock()
	on := !c.untilLocked(keyID).IsZero()
	if on {
		c.next++
	}
	id := c.next
	c.mu.Unlock()
	if !on {
		return nil
	}
	start := c.now()
	x := &Exchange{
		c: c, ID: id, KeyID: keyID, KeyName: keyName, At: start, Route: route,
		Client: Leg{
			Dialect: dialect,
			Started: start,
			Request: Message{
				Method: r.Method,
				URL:    redactURL(requestURL(r)),
				Proto:  r.Proto,
				Header: redact(r.Header),
			},
		},
	}
	x.Client.Request.Body.start = start
	x.Client.Request.Body.write(body)
	return x
}

func requestURL(r *http.Request) string {
	u := *r.URL
	if u.Host == "" {
		u.Host = r.Host
	}
	if u.Scheme == "" {
		u.Scheme = "http"
		if r.TLS != nil {
			u.Scheme = "https"
		}
	}
	return u.String()
}

// commit files a finished exchange, dropping the oldest past the budget.
func (c *Capture) commit(x *Exchange) {
	size := x.size()
	c.mu.Lock()
	c.held = append(c.held, x)
	c.byID[x.ID] = x
	if x.Event != 0 {
		c.byEvent[x.Event] = x
	}
	c.used += size
	x.bytes = size
	for c.used > c.budget && len(c.held) > 1 {
		c.forget(c.held[0])
		c.used -= c.held[0].bytes
		c.held[0] = nil
		c.held = c.held[1:]
	}
	p := c.persist
	c.mu.Unlock()
	c.hub.Changed(live.Captures)
	// A recording with no request log row has no id to be kept under; it
	// stays in memory only.
	if p != nil && x.Event != 0 {
		p.offer(x)
	}
}

// forget takes an exchange out of the indexes; under c.mu.
func (c *Capture) forget(x *Exchange) {
	delete(c.byID, x.ID)
	if c.byEvent[x.Event] == x {
		delete(c.byEvent, x.Event)
	}
}

// Summary is one exchange in a list: enough to find it, none of its content.
type Summary struct {
	ID       uint64    `json:"id"`
	At       time.Time `json:"at"`
	KeyID    string    `json:"key_id"`
	KeyName  string    `json:"key_name,omitempty"`
	Route    string    `json:"route"`
	Event    int64     `json:"event,omitempty"`
	Status   int       `json:"status"`
	Model    string    `json:"model,omitempty"`
	Provider string    `json:"provider,omitempty"`
	Attempts int       `json:"attempts"`
	Bytes    int64     `json:"bytes"`
}

// Summary is the exchange as a list shows it.
func (x *Exchange) Summary() Summary {
	return Summary{
		ID: x.ID, At: x.At, KeyID: x.KeyID, KeyName: x.KeyName, Route: x.Route, Event: x.Event,
		Status: x.Status, Model: x.Model, Provider: x.Provider, Attempts: len(x.Upstream), Bytes: x.bytes,
	}
}

// List is what is held for a key, or for every key when keyID is empty,
// the newest first.
func (c *Capture) List(keyID string) []Summary {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Summary, 0, len(c.held))
	for i := len(c.held) - 1; i >= 0; i-- {
		if x := c.held[i]; keyID == "" || x.KeyID == keyID {
			out = append(out, x.Summary())
		}
	}
	return out
}

// Get is one exchange. A held exchange is never written again, so it is
// returned as it is.
func (c *Capture) Get(id uint64) (*Exchange, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	x, ok := c.byID[id]
	return x, ok
}

// Exchanges is every exchange held for a key, or for every key, oldest
// first: what an export writes.
func (c *Capture) Exchanges(keyID string) []*Exchange {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*Exchange
	for _, x := range c.held {
		if keyID == "" || x.KeyID == keyID {
			out = append(out, x)
		}
	}
	return out
}

// Recorded is which of these request log rows have a recording, held or
// stored: for a list of requests to say which it can show the content of.
func (c *Capture) Recorded(ctx context.Context, events []int64) map[int64]bool {
	out := map[int64]bool{}
	c.mu.Lock()
	for _, e := range events {
		if _, ok := c.byEvent[e]; ok {
			out[e] = true
		}
	}
	p := c.persist
	c.mu.Unlock()
	if p == nil || len(out) == len(events) {
		return out
	}
	stored, err := p.db.Has(ctx, events)
	if err != nil {
		p.log.Warn("could not read which requests have stored recordings", "err", err)
		return out
	}
	for e := range stored {
		out[e] = true
	}
	return out
}

// ErrNotRecorded is a request with no recording held or stored.
var ErrNotRecorded = errors.New("no recording of that request")

// ByEvent is a request's recording by its request log row: held in memory,
// or read back from disk exactly as it was recorded.
func (c *Capture) ByEvent(ctx context.Context, event int64) (*Exchange, error) {
	c.mu.Lock()
	x, ok := c.byEvent[event]
	p := c.persist
	c.mu.Unlock()
	if ok {
		return x, nil
	}
	if p == nil {
		return nil, ErrNotRecorded
	}
	x, err := p.load(ctx, event)
	if errors.Is(err, recordings.ErrNotFound) {
		return nil, ErrNotRecorded
	}
	return x, err
}

// Clear drops what is held and stored for a key, or everything when keyID is
// empty.
func (c *Capture) Clear(ctx context.Context, keyID string) error {
	c.mu.Lock()
	kept := c.held[:0]
	c.used = 0
	for _, x := range c.held {
		if keyID != "" && x.KeyID != keyID {
			kept = append(kept, x)
			c.used += x.bytes
		} else {
			c.forget(x)
		}
	}
	for i := len(kept); i < len(c.held); i++ {
		c.held[i] = nil
	}
	c.held = kept
	p := c.persist
	c.mu.Unlock()
	c.hub.Changed(live.Captures)
	if p != nil {
		return p.db.Clear(ctx, keyID)
	}
	return nil
}

// Status is the recorder as a whole, for the admin UI.
type Status struct {
	Held   int   `json:"held"`
	Used   int64 `json:"used"`
	Budget int64 `json:"budget"`
	// Recording is when each recording key's recording ends.
	Recording map[string]time.Time `json:"recording"`
	// Stored is what is kept on disk, when recordings are.
	Stored *StoredStatus `json:"stored,omitempty"`
}

// StoredStatus is the recordings kept on disk.
type StoredStatus struct {
	recordings.Usage
	// Written is how many this process stored, and Dropped how many it did
	// not: the queue full, the disk nearly full, or a write failed.
	Written int64 `json:"written"`
	Dropped int64 `json:"dropped"`
}

// Status reports what is held and who is being recorded, and what is
// stored — read after the lock is let go, so no request waits on the disk.
func (c *Capture) Status() Status {
	c.mu.Lock()
	rec := map[string]time.Time{}
	for k := range c.until {
		if u := c.untilLocked(k); !u.IsZero() {
			rec[k] = u
		}
	}
	out := Status{Held: len(c.held), Used: c.used, Budget: c.budget, Recording: rec}
	p := c.persist
	c.mu.Unlock()
	if p != nil {
		u, err := p.db.Usage(context.Background())
		if err != nil {
			p.log.Warn("could not read the stored recordings' usage", "err", err)
		}
		out.Stored = &StoredStatus{Usage: u, Written: p.stored.Load(), Dropped: p.drops.Load()}
	}
	return out
}

// RunPruner keeps the stored recordings within keep and budget, every
// interval until stop closes. Only the worker serving runs it; its first round
// also removes the files an earlier release's format left (recordings,
// RemoveOthers), by when the worker before it has finished.
func (c *Capture) RunPruner(stop <-chan struct{}, every, keep time.Duration, budget int64) {
	c.mu.Lock()
	p := c.persist
	c.mu.Unlock()
	if p == nil {
		return
	}
	first := true
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		if first {
			first = false
			recordings.RemoveOthers(filepath.Dir(p.db.Path()))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		n, err := p.db.Prune(ctx, keep, budget)
		cancel()
		if err != nil {
			p.log.Warn("could not prune stored recordings", "err", err)
		} else if n > 0 {
			p.log.Info("pruned stored recordings", "removed", n)
		}
	}
}

// secretHeaders are the credentials a recorded header set must not carry: the
// client's key to the gateway, and the account's token to the upstream. An
// export is something people pass around.
var secretHeaders = map[string]bool{
	"authorization":       true,
	"proxy-authorization": true,
	"x-api-key":           true,
	"x-goog-api-key":      true,
	"cookie":              true,
	"set-cookie":          true,
}

// Redacted is what a secret header's value is recorded as.
const Redacted = "[redacted]"

func redact(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, vs := range h {
		if secretHeaders[strings.ToLower(k)] {
			out[k] = []string{Redacted}
			continue
		}
		out[k] = append([]string(nil), vs...)
	}
	return out
}

// redactURL drops a key passed in the query, which the Gemini API allows.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery == "" {
		return raw
	}
	q := u.Query()
	if q.Has("key") {
		q.Set("key", Redacted)
		u.RawQuery = q.Encode()
	}
	return u.String()
}
