package httpapi

import (
	"compress/gzip"
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

// fakeUI stands in for a built UI. The checkout used for tests has none
// embedded, so the site is rebuilt over an in-memory one before any listener's
// routes are built from it.
func fakeUI(srv *Server) (index, script string) {
	index = "<!doctype html><title>admin</title>" + strings.Repeat("<p>admin</p>", 200)
	script = strings.Repeat("console.log('compressible');\n", 200)
	files := fstest.MapFS{
		"webdist/index.html":         {Data: []byte(index)},
		"webdist/welcome.html":       {Data: []byte("<!doctype html><title>welcome</title>")},
		"webdist/assets/app-1a2b.js": {Data: []byte(script)},
		"webdist/favicon.png":        {Data: []byte("\x89PNG\r\n\x1a\nnot really")},
	}
	srv.web = web.New(web.Deps{Log: srv.log, Files: files})
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

// A relay-only listener's root is the welcome page: built from the UI when
// there is one, a plain page when there is not, and never the admin app. Every
// other path stays the API's, answering a JSON 404 a client can parse.
func TestRelayOnlyRootIsTheWelcomePage(t *testing.T) {
	srv, _, _ := newTestServer(t)
	relay := func() string {
		ts := httptest.NewServer(srv.routes(role{gateway: true}))
		t.Cleanup(ts.Close)
		return ts.URL
	}

	// No UI embedded: the fallback, still a page. Set explicitly rather than
	// taken from the checkout, which holds a real build once make has run.
	srv.web = web.New(web.Deps{Log: srv.log, Files: fstest.MapFS{}})
	base := relay()
	status, body := call(t, base, http.MethodGet, "/", "", nil)
	if status != http.StatusOK || !strings.Contains(string(body), "A private AI gateway") {
		t.Errorf("unbuilt welcome: %d %q", status, body)
	}

	fakeUI(srv)
	base = relay()
	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(page), "<title>welcome</title>") ||
		resp.Header.Get("Referrer-Policy") != "no-referrer" || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("welcome: %d %q %q", resp.StatusCode, resp.Header.Get("Cache-Control"), page)
	}
	// The files the page loads are served; the admin app's document is not.
	if status, _ := call(t, base, http.MethodGet, "/assets/app-1a2b.js", "", nil); status != http.StatusOK {
		t.Errorf("welcome asset: %d", status)
	}
	for _, p := range []string{"/index.html", "/welcome.html", "/accounts", "/docs", "/assets/app-old.js"} {
		if status, body := call(t, base, http.MethodGet, p, "", nil); status != http.StatusNotFound || errType(t, body) != "not_found" {
			t.Errorf("GET %s on the relay: %d %s", p, status, body)
		}
	}
	if status, _ := call(t, base, http.MethodPost, "/", `{}`, nil); status != http.StatusNotFound {
		t.Errorf("POST / on the relay: %d, want 404", status)
	}
}
