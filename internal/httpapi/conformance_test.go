package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Conformance with Anthropic's gateway compatibility contract, for the clauses
// that are about routing and endpoints rather than the relay.
//
//	https://code.claude.com/docs/en/llm-gateway-protocol

// "Inference requests post to `/v1/messages?beta=true`, so match on the path,
// not the full URL."
func TestContractRoutesInferenceDespiteTheQueryString(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	_, plaintext, err := st.CreateKey(context.Background(), "client", 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/v1/messages",
		"/v1/messages?beta=true",
		"/v1/messages?beta=true&something=else",
	} {
		t.Run(path, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, base+path,
				strings.NewReader(`{"model":"claude-haiku-4-5","messages":[]}`))
			req.Header.Set("X-Api-Key", plaintext)
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			// No account is connected, so this cannot succeed — but it must
			// reach the inference handler rather than falling through to the
			// static UI or a 404.
			if resp.StatusCode == http.StatusNotFound {
				t.Errorf("status = 404: the query string changed which handler ran")
			}
			if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "text/html") {
				t.Errorf("Content-Type = %q: the request fell through to the UI", ct)
			}
		})
	}
}

// "A gateway that responds slowly or redirects `/v1/models`, even `http` to
// `https`, fails discovery silently; serve the endpoint directly at the
// configured base URL."
//
// And: "The request is `GET /v1/models?limit=1000` with a 3-second timeout".
func TestContractModelDiscoveryNeitherRedirectsNorDawdles(t *testing.T) {
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"claude-opus-5","display_name":"Opus"}]}`)
	}))
	defer upstreamSrv.Close()

	srv, st, _ := newTestServer(t)
	srv.relay.BaseURL = upstreamSrv.URL
	if err := seedAccount(t, st, srv); err != nil {
		t.Fatal(err)
	}
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	_, plaintext, err := st.CreateKey(context.Background(), "client", 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	// A client that refuses to follow redirects, the way discovery does.
	client := &http.Client{
		Timeout: 3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/v1/models?limit=1000", nil)
	req.Header.Set("X-Api-Key", plaintext)

	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("discovery request failed: %v", err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(started)

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		t.Errorf("status = %d: any redirect is treated as failure", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, b)
	}
	if elapsed > 3*time.Second {
		t.Errorf("took %s; the client gives up at 3 seconds", elapsed)
	}

	// "Claude Code reads `id`, the optional `display_name`, and the optional
	// `description` from each entry in the response's `data` array"
	var body struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("response is not the documented shape: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != "claude-opus-5" {
		t.Errorf("data = %+v, want the upstream's entry relayed", body.Data)
	}
	if body.Data[0].DisplayName != "Opus" {
		t.Error("display_name was dropped")
	}
}

// "If the request fails or the gateway doesn't implement `/v1/models`, the
// picker falls back to the cached list from the previous startup or to the
// built-in model list."
//
// A failure has to look like a failure. A 200 carrying an empty data array is a
// successful answer meaning "no models", which replaces the cache instead of
// falling back to it.
func TestContractDiscoveryFailureIsNotAnEmptyList(t *testing.T) {
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstreamSrv.Close()

	srv, st, _ := newTestServer(t)
	srv.relay.BaseURL = upstreamSrv.URL
	if err := seedAccount(t, st, srv); err != nil {
		t.Fatal(err)
	}
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	_, plaintext, err := st.CreateKey(context.Background(), "client", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/v1/models?limit=1000", nil)
	req.Header.Set("X-Api-Key", plaintext)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = 200, body = %s; this overwrites the client's cached model list", b)
	}
}

// "Token-counting endpoints are the only optional ones: when they're absent,
// Claude Code falls back to counting context usage through the inference
// endpoint instead."
//
// Serving it is what stops arithmetic consuming inference requests.
func TestContractServesCountTokens(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	_, plaintext, err := st.CreateKey(context.Background(), "client", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/messages/count_tokens",
		strings.NewReader(`{"model":"claude-haiku-4-5","messages":[]}`))
	req.Header.Set("X-Api-Key", plaintext)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		t.Error("count_tokens is not served; Claude Code will spend inference requests on counting")
	}
}

// "An Anthropic Messages-format gateway receives a `HEAD /api/hello`
// connection-warming probe" — best-effort, and rejecting it breaks nothing,
// but answering keeps it out of the logs as an error.
func TestContractAnswersTheWarmupProbe(t *testing.T) {
	srv, _, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	req, _ := http.NewRequest(http.MethodHead, base+"/api/hello", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		t.Errorf("status = %d: the probe should not be a server error", resp.StatusCode)
	}
}

// "The developer's gateway credential, in one or both headers depending on
// which credential variable they set."
//
// Both spellings have to work, and discovery in particular sends both at once.
func TestContractAcceptsEitherCredentialHeader(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	_, plaintext, err := st.CreateKey(context.Background(), "client", 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	for name, set := range map[string]func(*http.Request){
		"x-api-key":     func(r *http.Request) { r.Header.Set("X-Api-Key", plaintext) },
		"authorization": func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+plaintext) },
		"both": func(r *http.Request) {
			r.Header.Set("X-Api-Key", plaintext)
			r.Header.Set("Authorization", "Bearer "+plaintext)
		},
	} {
		t.Run(name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
			set(req)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusUnauthorized {
				t.Errorf("credential in %s was not accepted", name)
			}
		})
	}
}
