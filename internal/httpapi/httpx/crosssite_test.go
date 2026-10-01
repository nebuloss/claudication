package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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
			if got := IsNavigation(req); got != tc.wantSpent {
				t.Errorf("IsNavigation = %v, want %v", got, tc.wantSpent)
			}
		})
	}
}
