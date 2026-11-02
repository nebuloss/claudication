package capture

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func request(t *testing.T, url string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, url, nil)
	r.Header.Set("Authorization", "Bearer sk-client")
	r.Header.Set("X-Goog-Api-Key", "goog")
	r.Header.Set("Content-Type", "application/json")
	return r
}

// A key not being recorded is not recorded, and nothing it does costs a
// branch at the caller: every method of the nil exchange does nothing.
func TestOffRecordsNothing(t *testing.T) {
	c := New(0)
	x := c.Begin("k", "client", "/v1/messages", "anthropic", request(t, "/v1/messages"), []byte(`{}`))
	if x != nil {
		t.Fatal("recorded a key nobody asked to record")
	}
	w := httptest.NewRecorder()
	if x.Writer(w) != w {
		t.Error("a nil exchange wrapped the writer")
	}
	x.Sent("anthropic", "a", request(t, "/up"), nil)
	x.Served("m", "p")
	x.Logged(1)
	x.Finish()
	if n := len(c.List("")); n != 0 {
		t.Errorf("%d held", n)
	}
}

// Recording ends at its time, by itself.
func TestRecordingEnds(t *testing.T) {
	c := New(0)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	until := c.Record("k", 10*time.Minute)
	if !until.Equal(now.Add(10 * time.Minute)) {
		t.Errorf("until = %s", until)
	}
	if c.Begin("k", "", "/r", "anthropic", request(t, "/r"), nil) == nil {
		t.Fatal("not recording while on")
	}
	now = now.Add(10 * time.Minute)
	if c.Begin("k", "", "/r", "anthropic", request(t, "/r"), nil) != nil {
		t.Error("still recording at its end")
	}
	if !c.Until("k").IsZero() {
		t.Error("Until reports a recording that ended")
	}
	if got := c.Record("k", 48*time.Hour); !got.Equal(now.Add(MaxFor)) {
		t.Errorf("a day and more asked = %s, want clamped to MaxFor", got)
	}
}

// One exchange: the client's leg with its answer, each attempt with its
// own, credentials taken out of headers and the query, and the bytes read
// through the tap the bytes the upstream sent.
func TestAnExchange(t *testing.T) {
	c := New(0)
	c.Record("k", time.Hour)
	x := c.Begin("k", "watched", "/v1beta/models/m:generateContent", "gemini",
		request(t, "http://gw/v1beta/models/m:generateContent?key=secret&alt=sse"), []byte(`{"contents":[]}`))

	w := httptest.NewRecorder()
	cw := x.Writer(w)
	if _, ok := cw.(interface{ Unwrap() http.ResponseWriter }); !ok {
		t.Error("the client's writer lost Unwrap, so flushing would not reach the connection")
	}

	up := httptest.NewRequest(http.MethodPost, "https://upstream/v1/messages", nil)
	up.Header.Set("Authorization", "Bearer account-token")
	x.Sent("anthropic", "one@example.com", up, []byte(`{"first":1}`))
	x.Failed(io.ErrUnexpectedEOF)
	x.Sent("anthropic", "two@example.com", up, []byte(`{"second":2}`))
	body := x.Answered(200, http.Header{"Content-Type": {"text/event-stream"}}, io.NopCloser(strings.NewReader("event: a\n\nevent: b\n\n")))
	relayed, _ := io.ReadAll(body)
	body.Close()
	if string(relayed) != "event: a\n\nevent: b\n\n" {
		t.Errorf("the tap changed the answer: %q", relayed)
	}

	cw.Header().Set("Content-Type", "application/json")
	cw.WriteHeader(201)
	_, _ = cw.Write([]byte(`{"ok":`))
	_, _ = cw.Write([]byte(`true}`))
	x.Served("claude-sonnet-5", "anthropic")
	x.Logged(42)
	x.Finish()
	x.Finish()

	held := c.List("k")
	if len(held) != 1 {
		t.Fatalf("held %d, want 1", len(held))
	}
	s := held[0]
	if s.Status != 201 || s.Event != 42 || s.Attempts != 2 || s.Model != "claude-sonnet-5" {
		t.Errorf("summary = %+v", s)
	}
	if !c.Recorded(t.Context(), []int64{42, 43})[42] {
		t.Error("event 42 is not reported recorded")
	}
	if x, err := c.ByEvent(t.Context(), 42); err != nil || x.ID != s.ID {
		t.Errorf("by event 42 = %v, %v", x, err)
	}
	got, _ := c.Get(s.ID)
	if h := got.Client.Request.Header.Get("Authorization"); h != Redacted {
		t.Errorf("client authorization recorded as %q", h)
	}
	if h := got.Client.Request.Header.Get("X-Goog-Api-Key"); h != Redacted {
		t.Errorf("client goog key recorded as %q", h)
	}
	if strings.Contains(got.Client.Request.URL, "secret") || !strings.Contains(got.Client.Request.URL, "alt=sse") {
		t.Errorf("client url recorded as %q", got.Client.Request.URL)
	}
	if string(got.Client.Response.Body.Bytes()) != `{"ok":true}` || got.Client.Response.Status != 201 {
		t.Errorf("client answer = %d %q", got.Client.Response.Status, got.Client.Response.Body.Bytes())
	}
	if n := len(got.Client.Response.Body.Chunks()); n != 2 {
		t.Errorf("%d parts timed, want 2", n)
	}
	first, second := got.Upstream[0], got.Upstream[1]
	if first.Error == "" || first.Response != nil || first.Account != "one@example.com" {
		t.Errorf("failed attempt = %+v", first)
	}
	if second.Request.Header.Get("Authorization") != Redacted || string(second.Request.Body.Bytes()) != `{"second":2}` {
		t.Errorf("second attempt's request = %v %q", second.Request.Header, second.Request.Body.Bytes())
	}
	if string(second.Response.Body.Bytes()) != string(relayed) || second.Ended.IsZero() {
		t.Errorf("second attempt's answer = %q", second.Response.Body.Bytes())
	}

	har, err := HAR(c.Exchanges(""), "claudication", "test")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Log struct {
			Entries []struct {
				Request struct {
					URL string `json:"url"`
				} `json:"request"`
				Own map[string]any `json:"_claudication"`
			} `json:"entries"`
		} `json:"log"`
	}
	if err := json.Unmarshal(har, &doc); err != nil {
		t.Fatal(err)
	}
	if n := len(doc.Log.Entries); n != 3 {
		t.Fatalf("%d HAR entries, want the client's and two attempts", n)
	}
	if bytes.Contains(har, []byte("account-token")) || bytes.Contains(har, []byte("sk-client")) {
		t.Error("the HAR carries a credential")
	}
}

// A body is cut at BodyCap and says so; past the budget the oldest
// exchanges go.
func TestBounds(t *testing.T) {
	c := New(3 * BodyCap)
	c.Record("k", time.Hour)
	big := bytes.Repeat([]byte("x"), BodyCap+10)
	for range 5 {
		x := c.Begin("k", "", "/r", "anthropic", request(t, "/r"), big)
		x.Finish()
	}
	held := c.Exchanges("k")
	if len(held) != 2 {
		t.Errorf("held %d, want what fits in three bodies' budget", len(held))
	}
	b := &held[0].Client.Request.Body
	if !b.Truncated() || len(b.Bytes()) != BodyCap || b.Size() != int64(len(big)) {
		t.Errorf("body kept %d of %d, truncated %v", len(b.Bytes()), b.Size(), b.Truncated())
	}
	if held[0].ID != 4 || held[1].ID != 5 {
		t.Errorf("kept %d and %d, want the newest", held[0].ID, held[1].ID)
	}
	_ = c.Clear(t.Context(), "k")
	if st := c.Status(); st.Held != 0 || st.Used != 0 {
		t.Errorf("after clearing: %+v", st)
	}
}

// Two requests that overlap finish in the other order from the one they
// started in, and both are found: the held list is in finishing order, the
// ids in starting order.
func TestOverlappingExchangesAreFound(t *testing.T) {
	c := New(0)
	c.Record("k", time.Hour)
	a := c.Begin("k", "", "/r", "anthropic", request(t, "/r"), []byte("a"))
	b := c.Begin("k", "", "/r", "anthropic", request(t, "/r"), []byte("b"))
	b.Finish()
	a.Finish()
	for _, x := range []*Exchange{a, b} {
		if got, ok := c.Get(x.ID); !ok || got != x {
			t.Errorf("exchange %d not found", x.ID)
		}
	}
	_ = c.Clear(t.Context(), "k")
	if _, ok := c.Get(a.ID); ok {
		t.Error("a cleared exchange is still found")
	}
}
