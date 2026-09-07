package oauth

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

// The exact request the client's revokeOAuthToken sends. Getting any of this
// wrong means the gateway believes it released a credential that is still live.
func TestRevokeSendsWhatTheClientSends(t *testing.T) {
	type body struct {
		Token         string `json:"token"`
		TokenTypeHint string `json:"token_type_hint"`
		ClientID      string `json:"client_id"`
	}

	var got body
	var gotAuth, gotCT, gotMethod string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := revokeAt(context.Background(), srv.Client(), srv.URL,
		"refresh-abc", "client-xyz"); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotCT)
	}
	// The token being revoked is the credential; the client sends nothing else.
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want no header", gotAuth)
	}
	if got.Token != "refresh-abc" {
		t.Errorf("token = %q", got.Token)
	}
	// The refresh token is what gets revoked. The access token is short-lived
	// and the client never sends it.
	if got.TokenTypeHint != "refresh_token" {
		t.Errorf("token_type_hint = %q, want refresh_token", got.TokenTypeHint)
	}
	if got.ClientID != "client-xyz" {
		t.Errorf("client_id = %q", got.ClientID)
	}
}

func TestRevokeDefaultsTheClientID(t *testing.T) {
	var got struct {
		ClientID string `json:"client_id"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
	}))
	defer srv.Close()

	if err := revokeAt(context.Background(), srv.Client(), srv.URL, "t", ""); err != nil {
		t.Fatal(err)
	}
	if got.ClientID != AnthropicClientID {
		t.Errorf("client_id = %q, want the Claude Code client id", got.ClientID)
	}
}

// An upstream that refuses has to be reported, because the caller logs it —
// but the caller deletes the account regardless, which is the point.
func TestRevokeReportsAnUpstreamRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_token"}`))
	}))
	defer srv.Close()

	err := revokeAt(context.Background(), srv.Client(), srv.URL, "t", "c")
	if err == nil {
		t.Fatal("a 400 was reported as success")
	}
	if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "invalid_token") {
		t.Errorf("error lost the upstream's own words: %v", err)
	}
}

// Nothing to revoke is not a failure: an account may predate the field, or the
// provider may have returned no refresh token at all.
func TestRevokeWithoutATokenDoesNothing(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer srv.Close()

	if err := revokeAt(context.Background(), srv.Client(), srv.URL, "", "c"); err != nil {
		t.Fatalf("empty token = %v, want nil", err)
	}
	if called {
		t.Error("called the endpoint with nothing to revoke")
	}
}

// The client allows itself 5 seconds. A delete must not hang on a hung
// endpoint, so a slow one has to come back as an error rather than block.
func TestRevokeIsBounded(t *testing.T) {
	// Released by the test rather than by the request context: the server does
	// not cancel a handler when the client gives up, so blocking on
	// r.Context().Done() leaves Close() waiting on it forever.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	defer func() { close(release); srv.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- revokeAt(ctx, srv.Client(), srv.URL, "t", "c") }()

	select {
	case err := <-done:
		if err == nil {
			t.Error("a hung endpoint reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("revoke did not give up")
	}
}
