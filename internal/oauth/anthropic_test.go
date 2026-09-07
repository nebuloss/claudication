package oauth

import (
	"strings"
	"testing"
)

func TestNewPKCEProducesValidChallenge(t *testing.T) {
	a, err := NewPKCE()
	if err != nil {
		t.Fatalf("NewPKCE: %v", err)
	}
	b, err := NewPKCE()
	if err != nil {
		t.Fatalf("NewPKCE: %v", err)
	}
	if a.Verifier == b.Verifier {
		t.Error("two verifiers must not be equal")
	}
	// RFC 7636 requires a verifier of 43-128 characters from the unreserved set.
	if len(a.Verifier) < 43 || len(a.Verifier) > 128 {
		t.Errorf("verifier length %d is outside the 43-128 range", len(a.Verifier))
	}
	if strings.ContainsAny(a.Verifier+a.Challenge, "+/=") {
		t.Error("PKCE values must be base64url without padding")
	}
	if a.Challenge == a.Verifier {
		t.Error("challenge must be the hash of the verifier, not the verifier")
	}
}

func TestAnthropicAuthURL(t *testing.T) {
	pkce := PKCE{Verifier: "v", Challenge: "chal"}
	got := AnthropicAuthURL("st4te", pkce, RedirectManual)

	// Pinned against a URL captured from `claude auth login --claudeai` on
	// 2.1.263 and confirmed working in a browser. Every deviation from this
	// shape that was tried — claude.ai as the host, scope moved last, colons
	// left unencoded — was rejected with "client_id: Field required", so this
	// is an exact-match assertion on purpose rather than a set of loose
	// Contains checks.
	want := "https://claude.com/cai/oauth/authorize" +
		"?code=true" +
		"&client_id=" + AnthropicClientID +
		"&response_type=code" +
		"&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback" +
		"&scope=org%3Acreate_api_key+user%3Aprofile+user%3Ainference" +
		"+user%3Asessions%3Aclaude_code+user%3Amcp_servers+user%3Afile_upload" +
		"&code_challenge=chal" +
		"&code_challenge_method=S256" +
		"&state=st4te"

	if got != want {
		t.Errorf("auth URL does not match the captured reference\n got:  %s\n want: %s", got, want)
	}
}

// The reference client emits 43 base64url characters of state; match it.
func TestNewStateLength(t *testing.T) {
	s, err := NewState()
	if err != nil {
		t.Fatal(err)
	}
	if len(s) != 43 {
		t.Errorf("state length = %d, want 43 (32 bytes base64url)", len(s))
	}
	if strings.ContainsAny(s, "+/=") {
		t.Errorf("state must be base64url without padding, got %q", s)
	}
}

// The consent screen rejects a malformed request outright, so the parameter
// order is pinned to the client's rather than left to map iteration or
// alphabetical sorting.
func TestAnthropicAuthURLParameterOrder(t *testing.T) {
	got := AnthropicAuthURL("st4te", PKCE{Challenge: "chal"}, RedirectManual)
	query := got[strings.Index(got, "?")+1:]

	var names []string
	for _, pair := range strings.Split(query, "&") {
		names = append(names, strings.SplitN(pair, "=", 2)[0])
	}
	want := []string{
		"code", "client_id", "response_type", "redirect_uri",
		"scope", "code_challenge", "code_challenge_method", "state",
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("parameter order = %v, want %v", names, want)
	}
}

func TestParseCallback(t *testing.T) {
	cases := map[string]struct {
		in        string
		wantCode  string
		wantState string
	}{
		"manual redirect page": {
			in:        "https://platform.claude.com/oauth/code/callback?code=abc123&state=xyz",
			wantCode:  "abc123",
			wantState: "xyz",
		},
		"loopback redirect URL": {
			in:        "http://localhost:54545/callback?code=abc123&state=xyz",
			wantCode:  "abc123",
			wantState: "xyz",
		},
		"bare code": {
			in:       "abc123",
			wantCode: "abc123",
		},
		// The consent screen sometimes joins the two with "#".
		"code#state in a URL": {
			in:        "http://localhost:54545/callback?code=abc123%23xyz",
			wantCode:  "abc123",
			wantState: "xyz",
		},
		"bare code#state": {
			in:        "abc123#xyz",
			wantCode:  "abc123",
			wantState: "xyz",
		},
		"surrounding whitespace": {
			in:       "  abc123  ",
			wantCode: "abc123",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			code, state, err := ParseCallback(tc.in)
			if err != nil {
				t.Fatalf("ParseCallback(%q): %v", tc.in, err)
			}
			if code != tc.wantCode {
				t.Errorf("code = %q, want %q", code, tc.wantCode)
			}
			if state != tc.wantState {
				t.Errorf("state = %q, want %q", state, tc.wantState)
			}
		})
	}
}

func TestParseCallbackRejects(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"whitespace only":  "   ",
		"URL without code": "http://localhost:54545/callback?state=xyz",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := ParseCallback(in); err == nil {
				t.Errorf("ParseCallback(%q) should have failed", in)
			}
		})
	}
}

// A denied consent must surface the provider's reason, not a generic failure.
func TestParseCallbackSurfacesOAuthError(t *testing.T) {
	_, _, err := ParseCallback(
		"http://localhost:54545/callback?error=access_denied&error_description=User+refused")
	if err == nil {
		t.Fatal("expected an error for a denied authorization")
	}
	if !strings.Contains(err.Error(), "User refused") {
		t.Errorf("error should carry the provider's description, got: %v", err)
	}
}
