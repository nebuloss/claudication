package store

import (
	"sync"
	"time"
)

// lastUseEvery is how often a key's or an account's last use is written.
const lastUseEvery = time.Minute

// lastUse remembers when each id's last use was last written, so a busy key
// or account costs one write a minute rather than one per request.
//
// TouchKey and MarkAccountUsed run on every relayed request, each an UPDATE
// that takes SQLite's single write lock beside the request's own usage row.
// Nobody reads last use finer than "a minute ago", so the column is allowed
// to lag by up to that.
//
// In memory, per process: a restart writes again on the first request, which
// is correct and costs one write. The zero value is ready to use.
type lastUse struct {
	mu sync.Mutex
	at map[string]time.Time
}

// due reports whether id's last use should be written now, and if so records
// that it is being.
func (l *lastUse) due(id string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if prev, ok := l.at[id]; ok && now.Sub(prev) < lastUseEvery {
		return false
	}
	if l.at == nil {
		l.at = map[string]time.Time{}
	}
	l.at[id] = now
	return true
}

// forget makes the next use of id write at once: after the row was deleted,
// or after something else wrote the row that the next use must overwrite.
func (l *lastUse) forget(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.at, id)
}
