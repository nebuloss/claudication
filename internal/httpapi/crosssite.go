package httpapi

import (
	"net/http"
	"net/url"
	"strings"
)

// sameSiteOnly refuses a state-changing request that the browser has told us
// came from somewhere else.
//
// The session cookie is SameSite=Strict, which already keeps a cross-site page
// from acting as a signed-in operator. What that does not cover is the endpoint
// that needs no cookie: POST /admin/setup claims a gateway nobody has set up
// yet, and any page the operator happens to visit can post to
// http://<gateway>:8317/admin/setup blind, with a password of its choosing, and
// take a fresh gateway on the LAN. The response is unreadable to it, but it
// does not need to read anything — it only needs to be first.
//
// The test is deliberately one-sided. A browser always sends Sec-Fetch-Site,
// so a request that says it is cross-site is refused; a request that sends
// neither Sec-Fetch-Site nor Origin is not a browser, and curl setting up a
// gateway from a script has to keep working. This closes the browser-driven
// attack without pretending to be a general authorisation check.
func sameSiteOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reason := offSite(r); reason != "" {
			writeError(w, http.StatusForbidden, "permission_error",
				"this request came from another site ("+reason+")")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isNavigation reports whether this looks like the browser loading a page
// rather than fetching a subresource of one.
//
// A request with no fetch metadata is treated as a navigation: that is curl,
// and `GET /admin/session?token=…` is a documented way to sign in.
func isNavigation(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Dest") {
	case "", "document":
		return true
	}
	return false
}

// offSite names why a request looks cross-site, or "" if it does not.
func offSite(r *http.Request) string {
	// Fetch metadata, where the browser states the relationship directly.
	// "none" is a user-typed URL or a bookmark; "same-origin" is our own page.
	switch site := r.Header.Get("Sec-Fetch-Site"); site {
	case "":
		// Not sent — fall through to Origin.
	case "none", "same-origin":
		return ""
	default:
		// "cross-site" and "same-site" both mean an origin that is not ours.
		return "Sec-Fetch-Site: " + site
	}

	origin := r.Header.Get("Origin")
	if origin == "" || origin == "null" {
		// No browser context to compare against.
		return ""
	}
	u, err := url.Parse(origin)
	if err != nil {
		return "unparseable Origin"
	}
	if !strings.EqualFold(u.Host, r.Host) {
		return "Origin: " + origin
	}
	return ""
}
