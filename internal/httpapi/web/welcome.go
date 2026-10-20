package web

import (
	"net/http"
	"path"
	"strings"

	"claudication/internal/httpapi/httpx"
)

// welcomeEntry is the welcome page's document in the build.
const welcomeEntry = "welcome.html"

// welcomeFallback is the root of a relay whose binary has no UI embedded.
//
// Self-contained on purpose: with no build there is no stylesheet to link and
// no script to read the status with, and a person who typed the address should
// still learn what it is rather than read a JSON error that looks like a fault.
//
// The built page's words, and its restraint: it is just as public, so it names
// no API, path, provider or client. TestWelcomeFallbackNamesNothing holds it
// to the same list scripts/check-welcome.mjs holds the build to.
const welcomeFallback = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex,nofollow">
<title>claudication</title>
<style>
  :root { color-scheme: light dark; }
  body { margin:0; min-height:100vh; display:grid; place-items:center;
         font:16px/1.6 system-ui,sans-serif; }
  main { max-width:32rem; padding:2rem 1.5rem; }
  h1 { margin:0 0 .5rem; font-size:1.25rem; font-weight:600; }
  p { margin:0 0 .5rem; opacity:.8; }
</style>
<main>
  <h1>A private AI gateway</h1>
  <p>This address belongs to a private service, run for a small group of
     people. If you were given access, use the key and the settings you were
     given. Otherwise there is nothing for you to do here.</p>
</main>
`

// WelcomeHandler serves the main page of a listener that carries no admin UI:
// the root, and the files of the build that page loads.
//
// Not the admin handler with another entry, because that one is a single-page
// app and answers every path-like miss with its document. Here every path but
// the root belongs to the API, and a client that asks for one it does not have
// must get a JSON 404 it can parse rather than a web page. Nor does it serve
// the build's other documents: index.html is the admin app, and on an address
// with no admin API it would draw a sign-in screen that can never sign in.
//
// A 200, unlike the 404 the root used to answer: there is a page here now, and
// /health is what a monitor watches.
func (s *Site) WelcomeHandler() http.Handler {
	assets := s.bundle()
	page, built := assets[welcomeEntry]

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		switch {
		case (clean == "." || clean == "") && built:
			serveFile(w, r, welcomeEntry, page)
			return
		case clean == "." || clean == "":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				_, _ = w.Write([]byte(welcomeFallback))
			}
			return
		}
		if a, ok := assets[clean]; ok && built && path.Ext(clean) != ".html" {
			serveFile(w, r, clean, a)
			return
		}
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such endpoint: "+r.URL.Path)
	})
}
