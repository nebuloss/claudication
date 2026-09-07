package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// The UI owns the origin root and the API owns its prefixes. Getting that
// split wrong is silent — an API client receives a web page and reports
// something that sounds nothing like a routing problem — so pin it.
func TestRootServesTheUI(t *testing.T) {
	srv, _, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /: status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want HTML", ct)
	}
	if got := resp.Header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

// The SPA needs a catch-all for its client-side routes, and that catch-all
// must not be allowed to answer for the API.
func TestUnknownAPIPathsAnswerJSON(t *testing.T) {
	srv, _, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	for _, path := range []string{"/v1/nope", "/v1/messages/typo", "/admin/nope", "/api/nope"} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want 404", path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("GET %s: Content-Type = %q, want JSON (a client cannot read HTML)", path, ct)
		}
		var envelope struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Errorf("GET %s: body is not an error envelope: %s", path, body)
			continue
		}
		if envelope.Error.Type != "not_found" {
			t.Errorf("GET %s: error type = %q", path, envelope.Error.Type)
		}
	}
}

// A client-side route is not a file, so it gets the app shell and lets the
// router sort it out.
func TestUnknownUIPathServesTheApp(t *testing.T) {
	srv, _, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	resp, err := http.Get(base + "/some/deep/route")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (the SPA shell)", resp.StatusCode)
	}
}

// A stale asset reference must 404 rather than receive HTML, which the browser
// would try to parse as JavaScript and fail somewhere unrelated.
func TestMissingAssetIsNotTheAppShell(t *testing.T) {
	srv, _, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	resp, err := http.Get(base + "/assets/index-deadbeef.js")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a missing asset", resp.StatusCode)
	}
}

// The proxy endpoints still take precedence over the catch-all, and still
// demand a credential rather than quietly serving a page.
func TestAPIStillOutranksTheCatchAll(t *testing.T) {
	srv, _, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	resp, err := http.Get(base + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /v1/models: status = %d, want 401", resp.StatusCode)
	}

	health, err := http.Get(base + "/health")
	if err != nil {
		t.Fatal(err)
	}
	health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Errorf("GET /health: status = %d, want 200", health.StatusCode)
	}
}

// A POST to a path the API does not claim is a client error, not a page.
func TestNonGETAtTheRootIsJSON(t *testing.T) {
	srv, _, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	resp := postJSON(t, base, http.MethodPost, "/whatever", map[string]string{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want JSON", ct)
	}
}
