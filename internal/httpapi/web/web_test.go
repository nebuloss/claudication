package web

import (
	"log/slog"
	"regexp"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
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

// The page a relay without a built UI answers its root with is as public as
// the built one, so it is held to the list scripts/check-welcome.mjs holds the
// build to. A name added there belongs here too.
func TestWelcomeFallbackNamesNothing(t *testing.T) {
	forbidden := []string{
		`(?i)claude`, `(?i)anthropic`, `(?i)openai`, `(?i)chatgpt`, `(?i)(^|[^a-z])gpt`,
		`(?i)codex`, `(?i)opencode`, `(?i)(^|[^a-z])crush`, `(?i)sonnet`, `(?i)(^|[^a-z])opus($|[^a-z])`,
		`(?i)haiku`, `(?i)gemini`, `/v1($|[^0-9])`, `/admin`, `(?i)oauth`, `(?i)x-api-key`,
		`(?i)subscription`, `(?i)upstream`, `(?i)(^|[^a-z])accounts?($|[^a-z])`, `/health`,
	}
	site := New(Deps{Log: slog.New(slog.DiscardHandler), Files: fstest.MapFS{}})
	w := get(t, site.WelcomeHandler(), http.MethodGet, "/", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("fallback: %d", w.Code)
	}
	for _, re := range forbidden {
		if loc := regexp.MustCompile(re).FindStringIndex(w.Body.String()); loc != nil {
			t.Errorf("the fallback page matches %s at %q", re, w.Body.String()[loc[0]:loc[1]])
		}
	}
}
