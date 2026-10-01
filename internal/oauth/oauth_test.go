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
