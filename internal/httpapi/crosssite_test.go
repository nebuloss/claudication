package httpapi

import (
	"net/http"
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

// A signed-in operator's cookie goes with a request from any page of the
// same site — another port of this host, a sibling subdomain — so a change
// must come from this origin too; reading is left alone.
func TestAdminRefusesASameSiteChange(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()
	setupAdmin(t, st)
	login := postJSON(t, base, http.MethodPost, "/admin/session", map[string]string{"password": testPassword})
	cookie := sessionCookieOf(t, login)
	login.Body.Close()

	for _, tc := range []struct {
		name, method, site string
		want               int
	}{
		{"same-site change", http.MethodPost, "same-site", http.StatusForbidden},
		{"same-origin change", http.MethodPost, "same-origin", http.StatusOK},
		{"same-site read", http.MethodGet, "same-site", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body *strings.Reader
			if tc.method == http.MethodPost {
				body = strings.NewReader(`{"enabled":true}`)
			} else {
				body = strings.NewReader("")
			}
			path := "/admin/gateway"
			if tc.method == http.MethodGet {
				path = "/admin/accounts"
			}
			req, _ := http.NewRequest(tc.method, base+path, body)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Sec-Fetch-Site", tc.site)
			req.AddCookie(cookie)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}
