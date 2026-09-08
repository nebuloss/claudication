package httpapi

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"embed"
)

// webdist holds the built admin UI. `make web` populates it; the .gitkeep
// placeholder keeps this directive valid in a source-only checkout, so the Go
// build never depends on Node having run.
//
//go:embed all:webdist
var webdist embed.FS

const notBuiltPage = `<!doctype html>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Admin UI not built</title>
<style>body{font:16px/1.6 system-ui,sans-serif;margin:0;display:grid;place-items:center;
min-height:100vh;background:#0d1417;color:#e2eae9}main{max-width:44rem;padding:2rem}
code{background:#1b262a;padding:.15em .4em;border-radius:.3em}</style>
<main><h1>Admin UI not built</h1>
<p>The gateway is running, but no compiled UI is embedded in this binary.</p>
<p>Build it with <code>make build</code>, or run the Vite dev server with
<code>make dev</code> and open it on port 5173.</p></main>`

// asset is one file of the embedded UI, prepared once at startup.
type asset struct {
	body []byte
	// gzipped is the same bytes compressed, or nil where compressing was not
	// worth it. The UI is ~222 KB of JS and CSS that gzips to ~66 KB, and it is
	// commonly fetched across a LAN or through an SSH tunnel — so the two
	// hundred milliseconds spent here at startup buy back two thirds of every
	// cold page load for the life of the process.
	gzipped []byte
	ctype   string
	etag    string
}

// compressible is by content type rather than extension: the question is
// whether the bytes have redundancy left, and the fingerprinted images and
// fonts Vite emits are already compressed formats that gzip only makes bigger.
func compressible(ctype string) bool {
	switch {
	case strings.HasPrefix(ctype, "text/"),
		strings.HasPrefix(ctype, "image/svg"):
		return true
	}
	switch {
	case strings.Contains(ctype, "javascript"),
		strings.Contains(ctype, "json"),
		strings.Contains(ctype, "xml"):
		return true
	}
	return false
}

func newAsset(name string, body []byte) asset {
	ctype := mime.TypeByExtension(path.Ext(name))
	if ctype == "" {
		ctype = http.DetectContentType(body)
	}
	sum := sha256.Sum256(body)
	a := asset{
		body:  body,
		ctype: ctype,
		etag:  `"` + hex.EncodeToString(sum[:16]) + `"`,
	}

	// Below about a packet there is nothing to win, and gzip's own header can
	// make a small file larger.
	if len(body) < 1024 || !compressible(ctype) {
		return a
	}
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return a
	}
	if _, err := zw.Write(body); err != nil || zw.Close() != nil {
		return a
	}
	if buf.Len() < len(body) {
		a.gzipped = buf.Bytes()
	}
	return a
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(name, "gzip") {
			return true
		}
	}
	return false
}

func (a asset) serve(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", a.ctype)
	h.Set("ETag", a.etag)
	// The response differs by Accept-Encoding, so any cache between here and
	// the browser has to key on it.
	h.Add("Vary", "Accept-Encoding")

	if match := r.Header.Get("If-None-Match"); match != "" {
		for _, tag := range strings.Split(match, ",") {
			if strings.TrimSpace(tag) == a.etag || strings.TrimSpace(tag) == "*" {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
	}

	body := a.body
	if a.gzipped != nil && acceptsGzip(r) {
		h.Set("Content-Encoding", "gzip")
		body = a.gzipped
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// staticHandler serves the embedded UI.
//
// A binary with no UI compiled in still starts and still proxies: the gateway's
// job is inference, and the admin screens are a convenience on top. Saying so
// on a page beats a bare 503 that looks like the whole thing is broken.
func (s *Server) staticHandler() http.Handler {
	root, err := fs.Sub(webdist, "webdist")
	if err != nil {
		s.log.Error("embedded UI unreadable", "err", err)
		return http.HandlerFunc(notBuilt)
	}
	if _, err := fs.ReadFile(root, "index.html"); err != nil {
		s.log.Warn("no embedded admin UI; serving the API only")
		return http.HandlerFunc(notBuilt)
	}

	// The whole UI is a few hundred kilobytes and never changes for the life of
	// the process, so it is read and compressed once rather than on every hit.
	assets := map[string]asset{}
	var raw, packed int
	err = fs.WalkDir(root, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := fs.ReadFile(root, name)
		if err != nil {
			return err
		}
		a := newAsset(name, body)
		assets[name] = a
		raw += len(body)
		if a.gzipped != nil {
			packed += len(a.gzipped)
		} else {
			packed += len(body)
		}
		return nil
	})
	if err != nil {
		s.log.Error("embedded UI unreadable", "err", err)
		return http.HandlerFunc(notBuilt)
	}
	s.log.Debug("admin UI prepared", "files", len(assets), "bytes", raw, "gzipped", packed)

	index := assets["index.html"]

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if clean == "." || clean == "" {
			clean = "index.html"
		}

		if a, ok := assets[clean]; ok {
			// Vite fingerprints everything under assets/, so those are safe to
			// cache forever; the entry document never is.
			if strings.HasPrefix(clean, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			a.serve(w, r)
			return
		}

		// A miss that looks like a file is a 404, not the index: answering a
		// stale asset reference with HTML gets it parsed as JavaScript, which
		// fails in a way that points nowhere near the real cause.
		if strings.Contains(path.Base(clean), ".") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		index.serve(w, r)
	})
}

func notBuilt(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && strings.Contains(path.Base(r.URL.Path), ".") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(notBuiltPage))
}
