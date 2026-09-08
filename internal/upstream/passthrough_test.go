package upstream

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"claudication/internal/pool"
)

// buildFor exercises the header rewriting in isolation.
func buildFor(t *testing.T, in *http.Request) *http.Request {
	t.Helper()
	r := &Relay{}
	out, err := r.build(in, "https://api.anthropic.com/v1/messages", []byte(`{"a":1}`), "tok123")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return out
}

// The contract forbids allowlisting: unknown anthropic-* headers and unknown
// beta values must reach the upstream untouched, or the next capability
// Anthropic ships breaks on the release that introduces it.
func TestBuildForwardsUnknownHeadersAndBetas(t *testing.T) {
	in := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	in.Header.Set("anthropic-beta", "some-future-beta-2030-01-01,another-one")
	in.Header.Set("anthropic-version", "2023-06-01")
	in.Header.Set("anthropic-something-new", "keep me")
	in.Header.Set("x-claude-code-session-id", "sess-1")
	in.Header.Set("x-stainless-lang", "js")

	out := buildFor(t, in)

	if got := out.Header.Get("anthropic-something-new"); got != "keep me" {
		t.Errorf("unknown anthropic header was dropped: %q", got)
	}
	if got := out.Header.Get("x-claude-code-session-id"); got != "sess-1" {
		t.Errorf("session id was dropped: %q", got)
	}
	if got := out.Header.Get("x-stainless-lang"); got != "js" {
		t.Errorf("client SDK header was dropped: %q", got)
	}

	betas := out.Header.Get("anthropic-beta")
	for _, want := range []string{"some-future-beta-2030-01-01", "another-one", oauthBeta} {
		if !strings.Contains(betas, want) {
			t.Errorf("anthropic-beta %q is missing %q", betas, want)
		}
	}
	if got := out.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q", got)
	}
}

// The OAuth capability is required on subscription traffic; stripping it 401s.
func TestBuildAddsOAuthBetaWhenAbsent(t *testing.T) {
	in := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	out := buildFor(t, in)
	if got := out.Header.Get("anthropic-beta"); got != oauthBeta {
		t.Errorf("anthropic-beta = %q, want %q", got, oauthBeta)
	}
}

// ...but it must not be duplicated if the client already sent it.
func TestBuildDoesNotDuplicateOAuthBeta(t *testing.T) {
	in := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	in.Header.Set("anthropic-beta", "claude-code-20250219,"+oauthBeta)
	out := buildFor(t, in)

	betas := out.Header.Get("anthropic-beta")
	if strings.Count(betas, oauthBeta) != 1 {
		t.Errorf("oauth beta appears %d times in %q", strings.Count(betas, oauthBeta), betas)
	}
}

// The caller's credential authenticates it to us, not us to Anthropic.
func TestBuildSwapsTheCredential(t *testing.T) {
	in := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	in.Header.Set("Authorization", "Bearer clc_client_key")
	in.Header.Set("x-api-key", "clc_client_key")

	out := buildFor(t, in)

	if got := out.Header.Get("Authorization"); got != "Bearer tok123" {
		t.Errorf("Authorization = %q, want the account token", got)
	}
	if out.Header.Get("x-api-key") != "" {
		t.Error("the client's x-api-key must not be forwarded")
	}
	if strings.Contains(out.Header.Get("Authorization"), "clc_") {
		t.Error("the client's own key leaked upstream")
	}
}

// Hop-by-hop headers belong to one connection and must not be relayed.
func TestBuildDropsHopByHop(t *testing.T) {
	in := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	in.Header.Set("Connection", "keep-alive")
	in.Header.Set("Upgrade", "h2c")
	in.Header.Set("Transfer-Encoding", "chunked")

	out := buildFor(t, in)
	for _, h := range []string{"Connection", "Upgrade", "Transfer-Encoding"} {
		if out.Header.Get(h) != "" {
			t.Errorf("hop-by-hop header %s was forwarded", h)
		}
	}
}

// The body must go out byte for byte.
func TestBuildSendsTheBodyVerbatim(t *testing.T) {
	in := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("ignored"))
	body := []byte(`{"model":"claude-opus-5","system":[{"type":"text","text":"x"}]}`)

	r := &Relay{}
	out, err := r.build(in, "https://api.anthropic.com/v1/messages", body, "tok")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(out.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Errorf("body was altered:\n got: %s\nwant: %s", got, body)
	}
}

func TestHasBeta(t *testing.T) {
	cases := map[string]bool{
		"":                        false,
		"oauth-2025-04-20":        true,
		"a, oauth-2025-04-20 ,b":  true,
		"OAUTH-2025-04-20":        true,
		"oauth-2025-04-20-extra":  false,
		"prefix-oauth-2025-04-20": false,
	}
	for header, want := range cases {
		if got := hasBeta(header, oauthBeta); got != want {
			t.Errorf("hasBeta(%q) = %v, want %v", header, got, want)
		}
	}
}

// 529 is Anthropic's documented overload code and must be retried on another
// account. auth2api omits it, so an overload takes the whole request down.
func TestClassifyStatus(t *testing.T) {
	cases := map[int]struct {
		kind      pool.FailureKind
		retryable bool
	}{
		200: {"", false},
		400: {"", false}, // the caller's request is wrong; do not blame the account
		404: {"", false},
		422: {"", false},
		401: {"auth", true},
		403: {"forbidden", true},
		429: {"rate_limit", true},
		500: {"server", true},
		503: {"server", true},
		529: {"server", true},
	}
	for status, want := range cases {
		kind, retryable := pool.ClassifyStatus(status)
		if string(kind) != string(want.kind) || retryable != want.retryable {
			t.Errorf("ClassifyStatus(%d) = %q,%v want %q,%v",
				status, kind, retryable, want.kind, want.retryable)
		}
	}
}
