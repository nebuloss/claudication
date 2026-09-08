package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// POST /admin/setup needs no cookie — that is the whole point of it, and the
// reason it needs a check of its own. Any page the operator visits can post to
// a gateway on their LAN and claim it with a password of the attacker's
// choosing; it cannot read the answer, but it does not need to.
func TestSetupRefusesACrossSitePost(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"cross-site", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"same-site", map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"foreign origin", map[string]string{"Origin": "http://evil.example"}, http.StatusForbidden},
		{"same-origin", map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusOK},
		{"typed in the bar", map[string]string{"Sec-Fetch-Site": "none"}, http.StatusOK},
		{"not a browser", nil, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh gateway per case: setup succeeds only once.
			srv, _, _ := newTestServer(t)
			base, cancel, done := startServer(t, srv)
			defer func() { cancel(); <-done }()

			req, _ := http.NewRequest(http.MethodPost, base+"/admin/setup",
				strings.NewReader(`{"password":"correct-horse"}`))
			req.Header.Set("Content-Type", "application/json")
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

// A sign-in link is single use, so anything that spends it without the
// operator having clicked it costs them the link. Every request under the
// static handler carries the query string the page was reached with.
func TestOnlyANavigationSpendsASignInLink(t *testing.T) {
	for _, tc := range []struct {
		name      string
		dest      string
		wantSpent bool
	}{
		{"a page load", "document", true},
		{"a prefetched script", "script", false},
		{"a favicon", "image", false},
		{"a stylesheet", "style", false},
		{"curl", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/?token=whatever", nil)
			if tc.dest != "" {
				req.Header.Set("Sec-Fetch-Dest", tc.dest)
			}
			if got := isNavigation(req); got != tc.wantSpent {
				t.Errorf("isNavigation = %v, want %v", got, tc.wantSpent)
			}
		})
	}
}
