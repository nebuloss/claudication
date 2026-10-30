// Package live is how a change reaches the admin UI: every change marks a
// topic, and every open tab subscribed to that topic is told — at most once
// per the topic's interval, however often it changes. docs/data-architecture.md
// has the rules; this is the hub they run on.
//
// A notice carries no data. The tab reads again, through the API, what the
// notice concerns: one source of truth, and a stream of a few bytes a
// second whatever the gateway's load.
package live

import (
	"sync"
	"sync/atomic"
	"time"
)

// Topic is one kind of data the admin UI shows. The list is the one place
// they are named; web/src/api/live.ts mirrors it, and a test holds the two
// together.
type Topic string

const (
	// Accounts: an account's row — order, name, switch, plan, tokens,
	// errors, last use — or one added or removed.
	Accounts Topic = "accounts"
	// Quota: an account's usage windows.
	Quota Topic = "quota"
	// Pool: cooldowns, model holds, and leases in flight.
	Pool Topic = "pool"
	// Models: models switched off, model checks, families, model lists.
	Models Topic = "models"
	// Clients: clients, keys, model names, limits, switches, install links.
	Clients Topic = "clients"
	// Running: requests running now, and in flight per client.
	Running Topic = "running"
	// Requests: the request log, and everything counted from it.
	Requests Topic = "requests"
	// Chats: chat titles.
	Chats Topic = "chats"
	// Settings: the gateway's switches.
	Settings Topic = "settings"
	// Uptime: uptime samples and outages.
	Uptime Topic = "uptime"
)

// Topics are every topic, in the order the UI lists them, each with the
// least time between two notices of it. A topic that changes per request is
// announced at most every interval however busy the gateway is; a rare one,
// at once.
var Topics = []struct {
	Topic    Topic
	Interval time.Duration
}{
	{Accounts, time.Second},
	{Quota, time.Second},
	{Pool, 2 * time.Second},
	{Models, time.Second},
	{Clients, time.Second},
	{Running, time.Second},
	{Requests, 2 * time.Second},
	{Chats, time.Second},
	{Settings, 0},
	{Uptime, 10 * time.Second},
}

// Tick is how often the hub announces what changed.
const Tick = 250 * time.Millisecond

// buffer is how many notices a stream holds before it is behind.
const buffer = 32

// Notice is what a stream is told: a topic and its version, or Resync.
type Notice struct {
	Seq     uint64
	Topic   Topic
	Version uint64
	// Resync is that notices were lost: read everything again.
	Resync bool
}

// Hub marks what changed and tells the streams subscribed to it. The zero
// value is not usable; New makes one, Run announces.
type Hub struct {
	idx map[Topic]int
	// version counts each topic's changes: raised by every Changed, so an
	// answer tagged with it is stale exactly when a change came after.
	version []atomic.Uint64
	wake    chan struct{}

	mu sync.Mutex
	// told is each topic's version when it was last announced; a topic is
	// due once its version has moved past it.
	told     []uint64
	lastSent []time.Time
	seq      uint64
	subs     map[*Sub]struct{}
	// closed is that Run has stopped: every stream is ended, and one opened
	// after is ended at once.
	closed bool
	// notices counts what each topic was announced, for the diagnostics.
	notices []uint64

	now func() time.Time
}

// New makes a hub over Topics.
func New() *Hub {
	h := &Hub{
		idx:      make(map[Topic]int, len(Topics)),
		version:  make([]atomic.Uint64, len(Topics)),
		wake:     make(chan struct{}, 1),
		told:     make([]uint64, len(Topics)),
		notices:  make([]uint64, len(Topics)),
		lastSent: make([]time.Time, len(Topics)),
		subs:     map[*Sub]struct{}{},
		now:      time.Now,
	}
	for i, t := range Topics {
		h.idx[t.Topic] = i
	}
	return h
}

// Changed marks topics changed. One atomic add each and a non-blocking
// wake: safe on the hottest path and under the caller's own lock. A nil hub
// marks nothing, so code built without one — a test, a command — need not
// check.
func (h *Hub) Changed(topics ...Topic) {
	if h == nil {
		return
	}
	for _, t := range topics {
		if i, ok := h.idx[t]; ok {
			h.version[i].Add(1)
		}
	}
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// Version is a topic's version now: raised by every change to it, whether
// announced yet or not.
func (h *Hub) Version(t Topic) uint64 {
	if h == nil {
		return 0
	}
	i, ok := h.idx[t]
	if !ok {
		return 0
	}
	return h.version[i].Load()
}

// Versions are several topics' versions, in the order asked: what a cached
// read is tagged with.
func (h *Hub) Versions(topics ...Topic) []uint64 {
	out := make([]uint64, len(topics))
	for i, t := range topics {
		out[i] = h.Version(t)
	}
	return out
}

// Sub is one tab's stream: the topics it shows, and its notices.
type Sub struct {
	C      <-chan Notice
	c      chan Notice
	topics map[Topic]bool
	// behind is that a notice could not be sent: Resync is owed.
	behind bool
}

// Subscribe opens a stream on topics. Close it with Unsubscribe. C is closed
// when the hub stops: the stream is over, and its tab should connect again —
// to the process that took over, on a reload.
func (h *Hub) Subscribe(topics []Topic) *Sub {
	c := make(chan Notice, buffer)
	s := &Sub{C: c, c: c, topics: map[Topic]bool{}}
	for _, t := range topics {
		if _, ok := h.idx[t]; ok {
			s.topics[t] = true
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		close(c)
		return s
	}
	h.subs[s] = struct{}{}
	return s
}

// Unsubscribe closes a stream.
func (h *Hub) Unsubscribe(s *Sub) {
	h.mu.Lock()
	delete(h.subs, s)
	h.mu.Unlock()
}

// Subscribers is how many streams are open.
func (h *Hub) Subscribers() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Watched reports whether an open stream is subscribed to any of topics:
// a tab showing them now.
func (h *Hub) Watched(topics ...Topic) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		for _, t := range topics {
			if s.topics[t] {
				return true
			}
		}
	}
	return false
}

// Run announces what changed every Tick, and at once when woken for a topic
// with no interval, until stop closes; then it ends every stream.
func (h *Hub) Run(stop <-chan struct{}) {
	t := time.NewTicker(Tick)
	defer t.Stop()
	for {
		select {
		case <-stop:
			h.close()
			return
		case <-t.C:
		case <-h.wake:
		}
		h.flush()
	}
}

// close ends every stream, and any opened from now on.
func (h *Hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for s := range h.subs {
		close(s.c)
		delete(h.subs, s)
	}
}

// flush announces each changed topic whose interval has passed.
func (h *Hub) flush() {
	now := h.now()
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, spec := range Topics {
		v := h.version[i].Load()
		if v == h.told[i] || now.Sub(h.lastSent[i]) < spec.Interval {
			continue
		}
		h.told[i] = v
		h.lastSent[i] = now
		h.notices[i]++
		h.seq++
		n := Notice{Seq: h.seq, Topic: spec.Topic, Version: v}
		for s := range h.subs {
			if !s.topics[spec.Topic] {
				continue
			}
			h.send(s, n)
		}
	}
	// A stream that fell behind is told to read everything again, once it
	// has room for the one notice that says so.
	for s := range h.subs {
		if s.behind {
			h.seq++
			select {
			case s.c <- Notice{Seq: h.seq, Resync: true}:
				s.behind = false
			default:
			}
		}
	}
}

// send tells a stream, or marks it behind: never blocking the hub on a slow
// tab, whose lost notices one Resync replaces.
func (h *Hub) send(s *Sub, n Notice) {
	if s.behind {
		return
	}
	select {
	case s.c <- n:
	default:
		s.behind = true
	}
}

// TopicStats is one topic as the diagnostics page shows it.
type TopicStats struct {
	Topic    Topic  `json:"topic"`
	Interval int64  `json:"interval_ms"`
	Version  uint64 `json:"version"`
	Notices  uint64 `json:"notices"`
	Watchers int    `json:"watchers"`
}

// Stats are every topic's changes, notices and streams since the hub
// started, and how many streams are open: what to look at when a screen is
// late, or the gateway busy.
func (h *Hub) Stats() ([]TopicStats, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]TopicStats, len(Topics))
	for i, spec := range Topics {
		out[i] = TopicStats{
			Topic: spec.Topic, Interval: spec.Interval.Milliseconds(),
			Version: h.version[i].Load(), Notices: h.notices[i],
		}
		for s := range h.subs {
			if s.topics[spec.Topic] {
				out[i].Watchers++
			}
		}
	}
	return out, len(h.subs)
}
