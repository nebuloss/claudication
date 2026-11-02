package capture

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"claudication/internal/secret"
	"claudication/internal/store/recordings"
)

func quiet() *slog.Logger { return slog.New(slog.DiscardHandler) }

func openFile(t *testing.T, dir string) *recordings.DB {
	t.Helper()
	db, err := recordings.Open(t.Context(), filepath.Join(dir, recordings.FileName(recordings.Version)), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func persisting(t *testing.T, dir string, db *recordings.DB) *Capture {
	t.Helper()
	sealer, err := secret.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := New(0)
	if err := c.Persist(t.Context(), db, sealer, quiet()); err != nil {
		t.Fatal(err)
	}
	return c
}

// One turn of a chat: the request, one attempt upstream, both answers, filed
// under its request log row.
func turn(t *testing.T, c *Capture, event int64, body []byte) *Exchange {
	t.Helper()
	x := c.Begin("k", "laptop", "/v1/messages", "anthropic", request(t, "http://gw/v1/messages"), body)
	x.Chat("chat-1")
	up := httptest.NewRequest(http.MethodPost, "https://upstream/v1/messages", nil)
	up.Header.Set("Authorization", "Bearer account-token")
	x.Sent("anthropic", "one@example.com", up, body)
	answer := x.Answered(200, http.Header{"Content-Type": {"text/event-stream"}},
		io.NopCloser(strings.NewReader("event: a\n\nevent: b\n\n")))
	_, _ = io.ReadAll(answer)
	answer.Close()
	w := x.Writer(httptest.NewRecorder())
	w.WriteHeader(200)
	_, _ = w.Write([]byte("event: a\n\n"))
	_, _ = w.Write([]byte("event: b\n\n"))
	x.Served("claude-sonnet-5", "anthropic")
	x.Logged(event)
	x.Finish()
	return x
}

// What is recorded outlives the process: read back by a new one, from the
// file alone, it is every leg, header and body exactly as recorded. A turn
// that resends the conversation costs about what it added, and a request with
// no row is not kept.
func TestRecordingsOutliveTheProcess(t *testing.T) {
	dir := t.TempDir()
	db := openFile(t, dir)
	c := persisting(t, dir, db)
	c.Record("k", time.Hour)

	first := randomBytes(300_000, 7)
	second := append(append([]byte{}, first...), randomBytes(10_000, 8)...)
	orig := turn(t, c, 7, first)
	c.Close(t.Context())
	u1, _ := db.Usage(t.Context())

	c = persisting(t, dir, db)
	c.Record("k", time.Hour)
	turn(t, c, 8, second)
	unrowed := c.Begin("k", "laptop", "/v1/messages", "anthropic", request(t, "/v1/messages"), []byte("no row"))
	unrowed.Finish()
	c.Close(t.Context())
	u2, _ := db.Usage(t.Context())
	if u2.Recordings != 2 {
		t.Fatalf("%d recordings stored, want the two with rows", u2.Recordings)
	}
	// Each turn carries its body twice (as sent and as relayed): the first
	// turn stores it once, the second only about what it added.
	if grew := u2.Content - u1.Content; grew > 10_000+4*chunkMax {
		t.Errorf("the second turn added %d bytes of content for 10000 new, the first %d", grew, u1.Content)
	}
	if u1.Content > int64(len(first))+4*chunkMax {
		t.Errorf("the first turn stored %d bytes for a %d byte body sent twice", u1.Content, len(first))
	}

	// A new process, from the file alone.
	again := persisting(t, dir, openFile(t, dir))
	got, err := again.ByEvent(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Stored() || got.Event != 7 || got.Conversation != "chat-1" || got.Model != "claude-sonnet-5" || got.Status != 200 {
		t.Errorf("read back = %+v", got)
	}
	if !bytes.Equal(got.Client.Request.Body.Bytes(), first) {
		t.Error("the client's body did not come back as recorded")
	}
	if got.Client.Request.Header.Get("Authorization") != Redacted {
		t.Errorf("client authorization stored as %q", got.Client.Request.Header.Get("Authorization"))
	}
	if string(got.Client.Response.Body.Bytes()) != "event: a\n\nevent: b\n\n" ||
		len(got.Client.Response.Body.Chunks()) != len(orig.Client.Response.Body.Chunks()) {
		t.Errorf("client answer = %q in %d parts", got.Client.Response.Body.Bytes(), len(got.Client.Response.Body.Chunks()))
	}
	if len(got.Upstream) != 1 || got.Upstream[0].Account != "one@example.com" ||
		!bytes.Equal(got.Upstream[0].Request.Body.Bytes(), first) ||
		got.Upstream[0].Request.Header.Get("Authorization") != Redacted {
		t.Errorf("upstream leg = %+v", got.Upstream)
	}
	rec := again.Recorded(t.Context(), []int64{7, 8, 9})
	if !rec[7] || !rec[8] || rec[9] {
		t.Errorf("recorded = %v", rec)
	}

	// Cleared, from the file too.
	if err := again.Clear(t.Context(), "k"); err != nil {
		t.Fatal(err)
	}
	if _, err := again.ByEvent(t.Context(), 8); err != ErrNotRecorded {
		t.Errorf("after clearing: %v", err)
	}
	if u, _ := db.Usage(context.Background()); u.Recordings != 0 || u.Content != 0 {
		t.Errorf("after clearing: %+v", u)
	}
}

// A file sealed under another key cannot be read, and is emptied rather
// than left to fail every read.
func TestAnotherKeyEmptiesTheFile(t *testing.T) {
	dir := t.TempDir()
	db := openFile(t, dir)
	c := persisting(t, dir, db)
	c.Record("k", time.Hour)
	turn(t, c, 7, []byte("a turn"))
	c.Close(t.Context())

	other := persisting(t, t.TempDir(), db) // another state dir: another key
	if other.Recorded(t.Context(), []int64{7})[7] {
		t.Error("a recording sealed under another key is still reported")
	}
}
