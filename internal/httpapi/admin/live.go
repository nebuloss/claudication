package admin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/live"
)

// How a tab learns what changed (docs/data-architecture.md): one stream of
// notices per tab, and reads that answer from what they last computed until a
// topic they read changes.

// streamKeepalive is how often a quiet stream writes a comment, so nothing
// between the tab and the gateway closes it as idle: nginx's default
// proxy_read_timeout is a minute.
const streamKeepalive = 15 * time.Second

// streamLife bounds one stream. The tab connects again at once, and the
// session is checked again then: a stream must not outlive the sign-in that
// opened it.
const streamLife = 30 * time.Minute

// handleEvents is a tab's stream: ?topics= names what its screen shows, and
// each change to one of them is one event naming the topic and its version.
// Nothing else is sent; the tab reads again what changed, through the API.
func (s *Admin) handleEvents(w http.ResponseWriter, r *http.Request) {
	var topics []live.Topic
	for t := range strings.SplitSeq(r.URL.Query().Get("topics"), ",") {
		topics = append(topics, live.Topic(strings.TrimSpace(t)))
	}
	sub := s.hub.Subscribe(topics)
	defer s.hub.Unsubscribe(sub)

	rc := http.NewResponseController(w)
	// The admin listener has no write timeout today; a stream must not
	// inherit one if it gains one.
	_ = rc.SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	// nginx buffers a proxied answer unless told not to, and a notice held
	// in its buffer is a screen that does not move.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// retry is how long the browser waits before connecting again: a reload
	// ends every stream, and the new worker is accepting already.
	if _, err := fmt.Fprint(w, "retry: 1000\n\n"); err != nil {
		return
	}
	_ = rc.Flush()

	keepalive := time.NewTicker(streamKeepalive)
	defer keepalive.Stop()
	life := time.NewTimer(streamLife)
	defer life.Stop()
	for {
		var err error
		select {
		case <-r.Context().Done():
			return
		case <-life.C:
			return
		case <-keepalive.C:
			_, err = fmt.Fprint(w, ": keepalive\n\n")
		case n, ok := <-sub.C:
			if !ok {
				return
			}
			if n.Resync {
				_, err = fmt.Fprintf(w, "id: %d\nevent: resync\ndata: {}\n\n", n.Seq)
			} else {
				_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: {\"v\":%d}\n\n", n.Seq, n.Topic, n.Version)
			}
		}
		if err != nil || rc.Flush() != nil {
			return
		}
	}
}

// handleLive is the diagnostics page: each topic's changes, notices and
// streams, and each cached read's hits and cost.
func (s *Admin) handleLive(w http.ResponseWriter, r *http.Request) {
	topics, streams := s.hub.Stats()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"topics":  topics,
		"streams": streams,
		"reads":   s.reads.stats(),
	})
}

// maxCached bounds how many answers are kept: one per URL, and a usage
// screen's URL carries its range.
const maxCached = 256

// readCache keeps expensive reads' answers, by URL.
type readCache struct {
	mu      sync.Mutex
	entries map[string]*cachedRead
	// byRoute is each wrapped route's hits, misses and cost, for the
	// diagnostics page.
	byRoute map[string]*readStats
}

type cachedRead struct {
	// mu is held while the answer is computed, so a burst of tabs reading
	// the same URL after one notice computes it once.
	mu       sync.Mutex
	versions []uint64
	at       time.Time
	used     time.Time
	header   http.Header
	body     []byte
	etag     string
}

type readStats struct {
	Route  string  `json:"route"`
	Topics string  `json:"topics"`
	Hits   uint64  `json:"hits"`
	Misses uint64  `json:"misses"`
	LastMS float64 `json:"last_ms"`
}

func (c *readCache) entry(key string) *cachedRead {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]*cachedRead{}
	}
	e, ok := c.entries[key]
	if !ok {
		if len(c.entries) >= maxCached {
			c.evictLocked()
		}
		e = &cachedRead{}
		c.entries[key] = e
	}
	return e
}

// evictLocked drops the answer read longest ago.
func (c *readCache) evictLocked() {
	var oldest string
	var at time.Time
	for k, e := range c.entries {
		// An entry being computed holds its lock; its time is read racily
		// otherwise, so skip any that cannot be locked now.
		if !e.mu.TryLock() {
			continue
		}
		used := e.used
		e.mu.Unlock()
		if oldest == "" || used.Before(at) {
			oldest, at = k, used
		}
	}
	delete(c.entries, oldest)
}

func (c *readCache) note(route string, topics []live.Topic, hit bool, cost time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byRoute == nil {
		c.byRoute = map[string]*readStats{}
	}
	st, ok := c.byRoute[route]
	if !ok {
		names := make([]string, len(topics))
		for i, t := range topics {
			names[i] = string(t)
		}
		st = &readStats{Route: route, Topics: strings.Join(names, ",")}
		c.byRoute[route] = st
	}
	if hit {
		st.Hits++
		return
	}
	st.Misses++
	st.LastMS = float64(cost.Microseconds()) / 1000
}

func (c *readCache) stats() []readStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]readStats, 0, len(c.byRoute))
	for _, st := range c.byRoute {
		out = append(out, *st)
	}
	return out
}

// cached answers an expensive read from what it last computed, while none of
// the topics it reads has changed since and the answer is younger than
// maxAge — which bounds what time alone changes, a cooldown ending or a
// window sliding. A tab that has the answer already is told 304.
//
// The versions are read before the answer is computed: a change while it is
// computed leaves the answer tagged older than it, and the next read
// computes it again. Only a 200 is kept.
func (s *Admin) cached(route string, topics []live.Topic, maxAge time.Duration, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		e := s.reads.entry(r.URL.RequestURI())
		e.mu.Lock()
		defer e.mu.Unlock()
		now := time.Now()
		versions := s.hub.Versions(topics...)
		hit := e.body != nil && slicesEqual(e.versions, versions) && now.Sub(e.at) < maxAge
		if hit {
			s.reads.note(route, topics, true, 0)
		} else {
			rec := &recorder{header: http.Header{}, status: http.StatusOK}
			h(rec, r)
			s.reads.note(route, topics, false, time.Since(now))
			if rec.status != http.StatusOK {
				copyHeader(w.Header(), rec.header)
				w.WriteHeader(rec.status)
				_, _ = w.Write(rec.body.Bytes())
				return
			}
			body := rec.body.Bytes()
			// The same answer keeps its tag, so a tab holding it is told 304
			// even after the answer was computed again.
			if e.body == nil || !bytes.Equal(e.body, body) {
				sum := sha256.Sum256(body)
				e.etag = `"` + hex.EncodeToString(sum[:12]) + `"`
			}
			e.versions, e.at, e.header, e.body = versions, now, rec.header, body
		}
		e.used = now
		copyHeader(w.Header(), e.header)
		// no-cache: the browser keeps the answer and asks whether it is
		// still current, which is the 304 below.
		w.Header().Set("Cache-Control", "private, no-cache")
		w.Header().Set("ETag", e.etag)
		if match := r.Header.Get("If-None-Match"); match != "" && match == e.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(e.body)
	}
}

func slicesEqual(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func copyHeader(dst, src http.Header) {
	for k, v := range src {
		dst[k] = v
	}
}

// recorder is a handler's answer kept rather than sent.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
	wrote  bool
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(status int) {
	if !r.wrote {
		r.status, r.wrote = status, true
	}
}

func (r *recorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.body.Write(b)
}
