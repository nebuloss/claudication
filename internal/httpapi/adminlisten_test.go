package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The point of splitting the listeners: publishing the relay must not publish
// the admin API or the UI with it. This asserts the separation is the
// gateway's own, so it holds however the proxy in front is configured.
func TestSplitListenersDoNotServeEachOther(t *testing.T) {
	s, _, _ := newTestServer(t)

	gateway := s.routes(role{gateway: true})
	adminOnly := s.routes(role{admin: true})

	// Every admin path, including the ones outside requireAdmin — those are
	// the dangerous ones, because they need no cookie. handleSetup on a fresh
	// gateway claims it outright.
	adminPaths := []struct{ method, path string }{
		{http.MethodGet, "/admin/setup"},
		{http.MethodPost, "/admin/setup"},
		{http.MethodPost, "/admin/session"},
		{http.MethodGet, "/admin/session"},
		{http.MethodGet, "/admin/accounts"},
		{http.MethodGet, "/admin/keys"},
		{http.MethodPost, "/admin/keys"},
		{http.MethodGet, "/admin/overview"},
		{http.MethodGet, "/admin/requests"},
	}
	for _, tc := range adminPaths {
		rec := httptest.NewRecorder()
		gateway.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s on the relay listener = %d, want 404",
				tc.method, tc.path, rec.Code)
		}
	}

	// And the UI, which is how an operator would reach any of it.
	for _, path := range []string{"/", "/index.html", "/assets/index.js"} {
		rec := httptest.NewRecorder()
		gateway.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s on the relay listener = %d, want 404", path, rec.Code)
		}
	}

	// A sign-in link must not be spendable there either: it is a credential,
	// and burning it against the wrong listener would be a confusing failure
	// on top of a security one.
	rec := httptest.NewRecorder()
	gateway.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?token=whatever", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("a sign-in link on the relay listener = %d, want 404", rec.Code)
	}

	// The other direction: the admin listener does not relay.
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/messages"},
		{http.MethodGet, "/v1/models"},
		{http.MethodPost, "/v1/messages/count_tokens"},
	} {
		rec := httptest.NewRecorder()
		adminOnly.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s on the admin listener = %d, want 404",
				tc.method, tc.path, rec.Code)
		}
	}
}

// Health answers on both, because whatever watches a listener needs something
// to watch, and it reveals nothing.
func TestHealthAnswersOnEitherListener(t *testing.T) {
	s, _, _ := newTestServer(t)
	for name, h := range map[string]http.Handler{
		"relay": s.routes(role{gateway: true}),
		"admin": s.routes(role{admin: true}),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s listener: GET /health = %d, want 200", name, rec.Code)
		}
	}
}

// Unset admin-listen keeps one listener serving everything, which is what
// every existing install has.
func TestCombinedListenerStillServesBoth(t *testing.T) {
	s, _, _ := newTestServer(t)
	both := s.routes(role{gateway: true, admin: true})

	rec := httptest.NewRecorder()
	both.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/setup", nil))
	if rec.Code == http.StatusNotFound {
		t.Error("GET /admin/setup = 404 on a combined listener")
	}

	rec = httptest.NewRecorder()
	both.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	if rec.Code == http.StatusNotFound {
		t.Error("POST /v1/messages = 404 on a combined listener")
	}
}
