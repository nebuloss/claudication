package httpapi

import (
	"io/fs"
	"net/http"
	"path"
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
	index, err := fs.ReadFile(root, "index.html")
	if err != nil {
		s.log.Warn("no embedded admin UI; serving the API only")
		return http.HandlerFunc(notBuilt)
	}
	files := http.FileServerFS(root)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if clean == "." || clean == "" {
			clean = "index.html"
		}

		if st, err := fs.Stat(root, clean); err == nil && !st.IsDir() {
			// Vite fingerprints everything under assets/, so those are safe to
			// cache forever; the entry document never is.
			if strings.HasPrefix(clean, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			files.ServeHTTP(w, r)
			return
		}

		// A miss that looks like a file is a 404, not the index: answering a
		// stale asset reference with HTML gets it parsed as JavaScript, which
		// fails in a way that points nowhere near the real cause.
		if strings.Contains(path.Base(clean), ".") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(index)
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
