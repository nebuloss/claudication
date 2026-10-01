package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

func get(t *testing.T, h http.Handler, method, path string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	for k, v := range header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestCompressionDecisions(t *testing.T) {
	for ctype, want := range map[string]bool{
		"text/html; charset=utf-8": true,
		"image/svg+xml":            true,
		"application/javascript":   true,
		"application/json":         true,
		"application/xml":          true,
		"image/png":                false,
		"font/woff2":               false,
	} {
		if got := compressible(ctype); got != want {
			t.Errorf("compressible(%q) = %v", ctype, got)
		}
	}
	for header, want := range map[string]bool{
		"gzip": true, "GZIP": true, "deflate, gzip;q=0.1": true, "br": false, "": false, "gzipx": false,
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Encoding", header)
		if got := acceptsGzip(r); got != want {
			t.Errorf("acceptsGzip(%q) = %v", header, got)
		}
	}
	// Small files stay as they are: gzip's own header would make them bigger.
	if a := newAsset("x.js", []byte("tiny()")); a.gzipped != nil {
		t.Error("a tiny file was compressed")
	}
	// An unknown extension falls back to sniffing.
	if a := newAsset("LICENSE", []byte("plain words")); !strings.HasPrefix(a.ctype, "text/plain") {
		t.Errorf("sniffed type = %q", a.ctype)
	}
}

// A binary built without the UI still proxies, and says why there is no UI
// rather than serving a bare error that looks like an outage.
func TestNoUIBuiltSaysSo(t *testing.T) {
	site := New(Deps{Log: slog.New(slog.DiscardHandler), Files: fstest.MapFS{}})
	h := site.StaticHandler("missing-entry.html")
	w := get(t, h, http.MethodGet, "/", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Admin UI not built") {
		t.Errorf("no UI: %d %q", w.Code, w.Body.String())
	}
	if w := get(t, h, http.MethodGet, "/app.js", nil); w.Code != http.StatusNotFound {
		t.Errorf("an asset with no UI: %d, want 404", w.Code)
	}
}

// The public page's model list is cached: the page is unauthenticated, and
// uncached anyone could spend the subscription by reloading it. A burst
// collapses into one fetch, and a failure keeps serving the last good answer.
func TestModelCache(t *testing.T) {
	var c modelCache
	var calls atomic.Int32
	release := make(chan struct{})
	fetch := func(context.Context) ([]byte, error) {
		calls.Add(1)
		<-release
		return []byte("v1"), nil
	}

	var wg sync.WaitGroup
	results := make([]string, 5)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = string(c.get(context.Background(), fetch))
		}()
	}
	// Let the burst pile up on the one in flight, then answer it.
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Errorf("a burst of 5 fetched %d times", n)
	}
	for _, r := range results {
		if r != "v1" {
			t.Errorf("a caller got %q", r)
		}
	}

	// Fresh: no fetch at all.
	if got := c.get(context.Background(), func(context.Context) ([]byte, error) {
		t.Error("fetched while fresh")
		return nil, nil
	}); string(got) != "v1" {
		t.Errorf("cached = %q", got)
	}

	// Expired and failing: the stale copy is served, and the next caller may
	// try again soon rather than after a whole TTL.
	c.mu.Lock()
	c.at = time.Now().Add(-2 * modelsTTL)
	c.mu.Unlock()
	failing := func(context.Context) ([]byte, error) { return nil, errors.New("upstream down") }
	if got := c.get(context.Background(), failing); string(got) != "v1" {
		t.Errorf("on failure = %q, want the stale copy", got)
	}
	c.mu.Lock()
	age := time.Since(c.at)
	c.mu.Unlock()
	if age < modelsTTL-2*time.Minute || age > modelsTTL {
		t.Errorf("after a failure the copy is %s old; the next caller should retry within a minute", age)
	}

	// Never fetched and failing: nothing, rather than an error.
	var empty modelCache
	if got := empty.get(context.Background(), failing); got != nil {
		t.Errorf("empty cache on failure = %q", got)
	}
}
