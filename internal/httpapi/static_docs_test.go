package httpapi

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"claudication/internal/httpapi/web"
)

// adminOnly serves the admin-only listener and returns its base URL.
func adminOnly(t *testing.T, srv *Server) string {
	t.Helper()
	ts := httptest.NewServer(srv.routes(role{admin: true}))
	t.Cleanup(ts.Close)
	return ts.URL
}

// docsOnly serves the public docs listener and returns its base URL.
func docsOnly(t *testing.T, srv *Server) string {
	t.Helper()
	ts := httptest.NewServer(srv.routes(role{docs: true}))
	t.Cleanup(ts.Close)
	return ts.URL
}

// fakeUI stands in for a built UI. The checkout used for tests has none
// embedded, so the site is rebuilt over an in-memory one before any listener's
// routes are built from it.
func fakeUI(srv *Server) (index, script string) {
	index = "<!doctype html><title>admin</title>" + strings.Repeat("<p>admin</p>", 200)
	script = strings.Repeat("console.log('compressible');\n", 200)
	srv.web = web.New(web.Deps{
		Config: srv.cfg, Log: srv.log, Store: srv.store, Docs: srv.docs,
		Surfaces: srv.surfaces, Models: srv.gateway.FetchModels,
		Files: fstest.MapFS{
			"webdist/index.html":         {Data: []byte(index)},
			"webdist/docs.html":          {Data: []byte("<!doctype html><title>docs</title>")},
			"webdist/assets/app-1a2b.js": {Data: []byte(script)},
			"webdist/favicon.png":        {Data: []byte("\x89PNG\r\n\x1a\nnot really")},
		},
	})
	return index, script
}

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

// The UI is fetched across tunnels and slow links, so it is compressed once
// and served gzipped to whoever asks; fingerprinted assets are cached forever
// and the entry document never is, or a deploy would be invisible until the
// cache expired.
func TestStaticAssetsAreCompressedAndCachedCorrectly(t *testing.T) {
	srv, _, _ := newTestServer(t)
	index, script := fakeUI(srv)
	h := srv.web.StaticHandler("index.html")

	w := get(t, h, http.MethodGet, "/assets/app-1a2b.js", map[string]string{"Accept-Encoding": "br, gzip;q=0.8"})
	if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("script: %d, encoding %q", w.Code, w.Header().Get("Content-Encoding"))
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("fingerprinted asset Cache-Control = %q", cc)
	}
	if !strings.Contains(w.Header().Get("Vary"), "Accept-Encoding") {
		t.Error("a negotiated response does not Vary on Accept-Encoding")
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(zr)
	if string(plain) != script {
		t.Error("the gzipped body does not inflate to the asset")
	}

	// Without gzip in Accept-Encoding the bytes go as they are.
	w = get(t, h, http.MethodGet, "/assets/app-1a2b.js", map[string]string{"Accept-Encoding": "identity"})
	if w.Header().Get("Content-Encoding") != "" || w.Body.String() != script {
		t.Error("an identity request got compressed bytes")
	}

	// The entry document: never cached without revalidation, conditional GET
	// honoured.
	w = get(t, h, http.MethodGet, "/", nil)
	if w.Code != http.StatusOK || w.Body.String() != index || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("index: %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the entry document")
	}
	for _, inm := range []string{etag, `"other", ` + etag, "*"} {
		if w := get(t, h, http.MethodGet, "/", map[string]string{"If-None-Match": inm}); w.Code != http.StatusNotModified {
			t.Errorf("If-None-Match %s: %d, want 304", inm, w.Code)
		}
	}
	if w := get(t, h, http.MethodGet, "/", map[string]string{"If-None-Match": `"stale"`}); w.Code != http.StatusOK {
		t.Errorf("a stale ETag: %d", w.Code)
	}

	// A client-side route resolves to the app; a missing file does not.
	if w := get(t, h, http.MethodGet, "/accounts/settings", nil); w.Body.String() != index {
		t.Error("a client-side route did not get the app shell")
	}
	if w := get(t, h, http.MethodGet, "/assets/app-old.js", nil); w.Code != http.StatusNotFound {
		t.Errorf("a stale asset reference: %d, want 404", w.Code)
	}

	// HEAD says how long without sending it.
	w = get(t, h, http.MethodHead, "/", nil)
	if w.Body.Len() != 0 || w.Header().Get("Content-Length") == "" {
		t.Errorf("HEAD: body %d bytes, Content-Length %q", w.Body.Len(), w.Header().Get("Content-Length"))
	}

	// Already-compressed formats are not gzipped again.
	if w := get(t, h, http.MethodGet, "/favicon.png", map[string]string{"Accept-Encoding": "gzip"}); w.Header().Get("Content-Encoding") != "" {
		t.Error("a PNG was gzipped")
	}
}

// The docs listener is public. Switched off it must say nothing about this
// gateway; switched on it serves the page and the one endpoint behind it, and
// never anything that can change state.
func TestDocsListener(t *testing.T) {
	srv, st, _ := newTestServer(t)
	srv.cfg.PublicURL = "https://relay.example"
	fakeUI(srv)
	fake := &fakeAnthropic{}
	fake.install(srv)
	base := docsOnly(t, srv)

	// Off: a readable 404 at the root, JSON everywhere else.
	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(page), "This is an API endpoint") ||
		resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("welcome: %d %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	for _, leak := range []string{"claudication", "version", "account"} {
		if strings.Contains(strings.ToLower(string(page)), leak) {
			t.Errorf("the welcome page mentions %q", leak)
		}
	}
	if status, body := call(t, base, http.MethodHead, "/", "", nil); status != http.StatusNotFound || len(body) != 0 {
		t.Errorf("HEAD welcome: %d, %d bytes", status, len(body))
	}
	if status, body := call(t, base, http.MethodGet, "/elsewhere", "", nil); status != http.StatusNotFound || errType(t, body) != "not_found" {
		t.Errorf("off, elsewhere: %d %s", status, body)
	}
	if status, _ := call(t, base, http.MethodGet, "/api/docs", "", nil); status != http.StatusNotFound {
		t.Errorf("/api/docs while off: %d", status)
	}

	if err := srv.docs.Set(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := seedAccount(t, st, srv); err != nil {
		t.Fatal(err)
	}

	resp, err = http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(page), "<title>docs</title>") ||
		resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("docs page: %d %q", resp.StatusCode, page)
	}

	status, body := call(t, base, http.MethodGet, "/api/docs", "", nil)
	if status != http.StatusOK {
		t.Fatalf("/api/docs: %d %s", status, body)
	}
	info := decode[map[string]any](t, body)
	if info["public_url"] != "https://relay.example" || info["ready"] != true || info["models"] == nil {
		t.Errorf("docs info = %v", info)
	}
	for _, leak := range []string{"listen", "state_dir", "trusted_proxies", "accounts", "keys"} {
		if _, ok := info[leak]; ok {
			t.Errorf("the public endpoint leaks %q", leak)
		}
	}
	// A second load does not spend another upstream call.
	call(t, base, http.MethodGet, "/api/docs", "", nil)
	if n := fake.models(); n != 1 {
		t.Errorf("model list fetched %d times for two page loads", n)
	}

	// Nothing on this listener changes anything.
	for _, ep := range []struct{ method, path string }{
		{http.MethodPost, "/admin/setup"},
		{http.MethodGet, "/admin/accounts"},
		{http.MethodPost, "/v1/messages"},
		{http.MethodPost, "/"},
	} {
		if status, _ := call(t, base, ep.method, ep.path, `{}`, nil); status != http.StatusNotFound {
			t.Errorf("%s %s on the docs listener: %d, want 404", ep.method, ep.path, status)
		}
	}
}

// A relay-only listener answers 404 at the root: it has nothing to show a
// browser and must not hint that an admin UI exists somewhere.
func TestRelayOnlyRootIsNotFound(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ts := httptest.NewServer(srv.routes(role{gateway: true}))
	defer ts.Close()
	if status, body := call(t, ts.URL, http.MethodGet, "/", "", nil); status != http.StatusNotFound || errType(t, body) != "not_found" {
		t.Errorf("relay root: %d %s", status, body)
	}
}
