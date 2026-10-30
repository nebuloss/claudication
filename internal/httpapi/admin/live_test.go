package admin

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"claudication/internal/live"
)

// A cached read computes once per change to a topic it reads, tells a tab
// holding the answer 304, and keeps the tag of an answer computed again the
// same.
func TestCachedReadFollowsItsTopics(t *testing.T) {
	s := &Admin{hub: live.New()}
	computed, body := 0, "a"
	h := s.cached("test", []live.Topic{live.Accounts}, time.Hour, func(w http.ResponseWriter, r *http.Request) {
		computed++
		_, _ = w.Write([]byte(body))
	})
	get := func(etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/admin/x", nil)
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}

	first := get("")
	etag := first.Header().Get("ETag")
	if first.Body.String() != "a" || etag == "" || computed != 1 {
		t.Fatalf("first read: %q, etag %q, computed %d", first.Body.String(), etag, computed)
	}
	if w := get(""); w.Body.String() != "a" || computed != 1 {
		t.Errorf("read again with nothing changed computed it again (%d)", computed)
	}
	if w := get(etag); w.Code != http.StatusNotModified {
		t.Errorf("a tab holding the answer got %d, want 304", w.Code)
	}

	s.hub.Changed(live.Requests) // a topic it does not read
	get("")
	if computed != 1 {
		t.Error("a change to another topic computed it again")
	}

	s.hub.Changed(live.Accounts)
	if w := get(etag); w.Code != http.StatusNotModified || computed != 2 {
		t.Errorf("same answer after a change: %d, computed %d; want 304 and 2", w.Code, computed)
	}
	body = "b"
	s.hub.Changed(live.Accounts)
	if w := get(etag); w.Code != http.StatusOK || w.Body.String() != "b" || w.Header().Get("ETag") == etag {
		t.Errorf("new answer: %d %q etag %q", w.Code, w.Body.String(), w.Header().Get("ETag"))
	}
}

// What is not a 200 is passed on and not kept.
func TestCachedReadKeepsOnlySuccess(t *testing.T) {
	s := &Admin{hub: live.New()}
	computed := 0
	h := s.cached("test", []live.Topic{live.Accounts}, time.Hour, func(w http.ResponseWriter, r *http.Request) {
		computed++
		w.WriteHeader(http.StatusInternalServerError)
	})
	for range 2 {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest("GET", "/admin/x", nil))
		if w.Code != http.StatusInternalServerError {
			t.Errorf("status %d", w.Code)
		}
	}
	if computed != 2 {
		t.Errorf("a failure was kept: computed %d times", computed)
	}
}

// The stream says each change to a topic it names, as an event named for the
// topic, and ends when the hub stops.
func TestEventsStream(t *testing.T) {
	s := &Admin{hub: live.New()}
	stop := make(chan struct{})
	go s.hub.Run(stop)
	srv := httptest.NewServer(http.HandlerFunc(s.handleEvents))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"?topics=settings,nonsense", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	lines := bufio.NewScanner(resp.Body)
	next := func() string {
		for lines.Scan() {
			if l := lines.Text(); strings.HasPrefix(l, "event: ") {
				return l
			}
		}
		return ""
	}
	for s.hub.Subscribers() == 0 {
		time.Sleep(time.Millisecond)
	}
	s.hub.Changed(live.Accounts, live.Settings)
	if got := next(); got != "event: settings" {
		t.Errorf("got %q, want the settings event and only it", got)
	}
	close(stop)
	if got := next(); got != "" {
		t.Errorf("after the hub stopped: %q, want the stream ended", got)
	}
}
