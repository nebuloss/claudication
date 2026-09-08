package httpapi

import (
	"sync"
	"time"
)

// limiter is a token bucket keyed by an arbitrary string.
//
// Unlike auth2api's fixed 60/min-per-IP counter, the primary key here is the
// API key, so one noisy client cannot throttle everyone sharing an egress IP,
// and the per-request budget can differ per credential.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time // injectable for tests
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter() *limiter {
	return &limiter{buckets: make(map[string]*bucket), now: time.Now}
}

// allow consumes one token for key, refilling at perMinute tokens per minute.
// A perMinute of zero or less disables limiting for that caller.
func (l *limiter) allow(key string, perMinute int) bool {
	return l.take(key, perMinute, true)
}

// peek reports whether a token is available without spending one.
//
// This exists so a check can happen before the work that decides whether the
// caller should be charged at all. The anonymous budget guards unauthenticated
// requests, but whether a request is authenticated is only known after a
// database lookup — so peek runs first (an IP that has already burned its
// budget never reaches the database) and allow runs afterwards, on the requests
// that turned out to be anonymous.
func (l *limiter) peek(key string, perMinute int) bool {
	return l.take(key, perMinute, false)
}

func (l *limiter) take(key string, perMinute int, spend bool) bool {
	if perMinute <= 0 {
		return true
	}
	rate := float64(perMinute) / 60.0 // tokens per second
	burst := float64(perMinute)

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		// A fresh caller starts full, minus the request being served.
		if !spend {
			return true
		}
		l.buckets[key] = &bucket{tokens: burst - 1, last: now}
		return true
	}

	b.tokens += now.Sub(b.last).Seconds() * rate
	if b.tokens > burst {
		b.tokens = burst
	}
	b.last = now

	if b.tokens < 1 {
		return false
	}
	if spend {
		b.tokens--
	}
	return true
}

// sweep drops buckets untouched for longer than idle, so the map cannot grow
// without bound across a long-running process.
func (l *limiter) sweep(idle time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := l.now().Add(-idle)
	for k, b := range l.buckets {
		if b.last.Before(cutoff) {
			delete(l.buckets, k)
		}
	}
}

func (l *limiter) runSweeper(stop <-chan struct{}, every, idle time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			l.sweep(idle)
		}
	}
}
