package live

import (
	"os"
	"regexp"
	"testing"
	"time"
)

// clock is a hub whose time the test moves.
func clock(h *Hub) *time.Time {
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	return &now
}

func drain(s *Sub) []Notice {
	var out []Notice
	for {
		select {
		case n := <-s.C:
			out = append(out, n)
		default:
			return out
		}
	}
}

// Many changes in one interval are one notice, to the streams subscribed to
// the topic and no other; the next comes once the interval has passed, and
// only if the topic changed again.
func TestChangesAreCoalescedPerInterval(t *testing.T) {
	h := New()
	now := clock(h)
	req := h.Subscribe([]Topic{Requests})
	acc := h.Subscribe([]Topic{Accounts})

	for range 100 {
		h.Changed(Requests)
	}
	h.flush()
	got := drain(req)
	if len(got) != 1 || got[0].Topic != Requests || got[0].Version != 100 {
		t.Fatalf("after 100 changes: %+v", got)
	}
	if n := drain(acc); len(n) != 0 {
		t.Errorf("a stream not on the topic was told: %+v", n)
	}

	h.Changed(Requests)
	*now = now.Add(time.Second)
	h.flush()
	if n := drain(req); len(n) != 0 {
		t.Errorf("told again inside the interval: %+v", n)
	}
	*now = now.Add(time.Second)
	h.flush()
	if n := drain(req); len(n) != 1 || n[0].Version != 101 {
		t.Errorf("after the interval: %+v", n)
	}
	*now = now.Add(time.Minute)
	h.flush()
	if n := drain(req); len(n) != 0 {
		t.Errorf("told with nothing changed: %+v", n)
	}
	if h.Version(Requests) != 101 {
		t.Errorf("version %d, want 101", h.Version(Requests))
	}
}

// A stream that cannot keep up never holds the hub: what it missed is one
// Resync, sent once it has room.
func TestASlowStreamIsToldToResync(t *testing.T) {
	h := New()
	now := clock(h)
	s := h.Subscribe([]Topic{Settings})
	for range buffer + 5 {
		h.Changed(Settings)
		*now = now.Add(time.Second)
		h.flush()
	}
	if got := drain(s); len(got) != buffer {
		t.Fatalf("%d notices held, want the buffer's %d", len(got), buffer)
	}
	h.Changed(Settings)
	h.flush()
	got := drain(s)
	if len(got) != 1 || !got[0].Resync {
		t.Errorf("after falling behind: %+v, want one Resync", got)
	}
}

// Watched is a stream open on any of the topics asked about.
func TestWatched(t *testing.T) {
	h := New()
	if h.Watched(Quota) {
		t.Error("watched with no stream")
	}
	s := h.Subscribe([]Topic{Requests, Quota})
	if !h.Watched(Accounts, Quota) || h.Watched(Accounts) {
		t.Error("watched does not follow the stream's topics")
	}
	h.Unsubscribe(s)
	if h.Watched(Quota) || h.Subscribers() != 0 {
		t.Error("still watched after the stream closed")
	}
	var none *Hub
	none.Changed(Quota) // a nil hub marks nothing, and does not panic
}

// A hub that stops ends every stream, and any opened after it: what moves
// a tab to the process that took over.
func TestStopEndsEveryStream(t *testing.T) {
	h := New()
	s := h.Subscribe([]Topic{Accounts})
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { h.Run(stop); close(done) }()
	close(stop)
	<-done
	if _, ok := <-s.C; ok {
		t.Error("a stream outlived the hub")
	}
	if _, ok := <-h.Subscribe([]Topic{Accounts}).C; ok {
		t.Error("a stream opened after the hub stopped is open")
	}
	if h.Subscribers() != 0 {
		t.Error("streams counted after the hub stopped")
	}
}

// The UI's list of topics is this one: a topic added here and not there is
// never subscribed to, and one there and not here is never announced.
func TestTopicsMirrored(t *testing.T) {
	src, err := os.ReadFile("../../web/src/api/live.ts")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)export const TOPICS = \[(.*?)\]`).FindSubmatch(src)
	if m == nil {
		t.Fatal("web/src/api/live.ts has no TOPICS list")
	}
	var ui []string
	for _, q := range regexp.MustCompile(`'([a-z]+)'`).FindAllSubmatch(m[1], -1) {
		ui = append(ui, string(q[1]))
	}
	if len(ui) != len(Topics) {
		t.Fatalf("the UI lists %v, the hub %d topics", ui, len(Topics))
	}
	for i, spec := range Topics {
		if ui[i] != string(spec.Topic) {
			t.Errorf("topic %d: the UI says %q, the hub %q", i, ui[i], spec.Topic)
		}
	}
}
