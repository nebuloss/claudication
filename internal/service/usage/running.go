package usage

import (
	"sort"
	"sync"
	"time"
)

// Running is the requests the gateway is serving now, for the request log to
// show above the ones it has recorded.
//
// The log is written when a request ends, so a request still running was in
// it nowhere: one waiting a minute and a half on a model's first token, which
// is the request an operator comes to the log about, showed only once it had
// failed or finished. Kept in memory and never written down: a request that
// is running has nothing to keep yet, and its row is the log's when it ends.
type Running struct {
	mu   sync.Mutex
	next uint64
	open map[uint64]RunningRequest
}

// RunningRequest is one request being served, as far as it is known when it
// is sent: what an operator reads in the log's row, less what only its end
// says.
type RunningRequest struct {
	StartedAt time.Time `json:"started_at"`
	KeyID     string    `json:"key_id,omitempty"`
	KeyName   string    `json:"key_name,omitempty"`
	// Model is what it is sent as, and Slot the client's name for it.
	Model        string `json:"model"`
	Slot         string `json:"virtual_model,omitempty"`
	Provider     string `json:"provider,omitempty"`
	Path         string `json:"path"`
	Streaming    bool   `json:"streaming"`
	Conversation string `json:"conversation_id,omitempty"`
	Client       string `json:"client,omitempty"`
	Surface      string `json:"surface,omitempty"`
	IP           string `json:"ip,omitempty"`
}

// Start notes a request being served, and returns what ends it.
func (r *Running) Start(q RunningRequest) (done func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.open == nil {
		r.open = map[uint64]RunningRequest{}
	}
	r.next++
	id := r.next
	r.open[id] = q
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.open, id)
			r.mu.Unlock()
		})
	}
}

// List is the requests running now, the longest-running first: the one an
// operator is looking for is the one that has been waiting. keyID, when not
// empty, keeps one client's.
func (r *Running) List(keyID string) []RunningRequest {
	r.mu.Lock()
	out := make([]RunningRequest, 0, len(r.open))
	for _, q := range r.open {
		if keyID == "" || q.KeyID == keyID {
			out = append(out, q)
		}
	}
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}
