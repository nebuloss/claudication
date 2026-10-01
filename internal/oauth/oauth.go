// Package oauth is the provider-neutral half of an OAuth login: PKCE, state,
// reading back what the operator pasted, and what a token exchange yields.
//
// Each provider's own client — its endpoints, client id, scopes and the
// quirks measured against them — lives with that provider, in
// internal/provider/<name>. What is here is what any of them would share.
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ErrInvalidGrant is the refusal that never comes right on its own: the
// refresh token has been revoked, has expired, or was already spent. Retrying
// it is not patience, it is a loop — only a human at a browser can fix it.
var ErrInvalidGrant = errors.New("the refresh token is no longer valid")

// PKCE is a verifier/challenge pair (RFC 7636, S256).
type PKCE struct {
	Verifier  string
	Challenge string
}

func base64URL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func NewPKCE() (PKCE, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return PKCE{}, fmt.Errorf("generate PKCE verifier: %w", err)
	}
	verifier := base64URL(raw)
	sum := sha256.Sum256([]byte(verifier))
	return PKCE{Verifier: verifier, Challenge: base64URL(sum[:])}, nil
}

// NewState returns 32 random bytes as 43 base64url characters, matching the
// length the reference client emits.
func NewState() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate state: %w", err)
	}
	return base64URL(raw), nil
}

// ParseCallback extracts the authorization code and state from whatever the
// operator pasted back: a full redirect URL, a bare "code#state" pair, or just
// the code.
//
// Claude's consent screen sometimes hands back the code with the state joined
// by "#", so both shapes have to work or the paste-based flow fails for
// reasons the operator cannot see.
func ParseCallback(input string) (code, state string, err error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", fmt.Errorf("nothing to parse: paste the URL you were redirected to")
	}

	if strings.Contains(input, "://") {
		u, perr := url.Parse(input)
		if perr != nil {
			return "", "", fmt.Errorf("that does not look like a URL: %w", perr)
		}
		q := u.Query()
		if e := q.Get("error"); e != "" {
			desc := q.Get("error_description")
			if desc == "" {
				desc = e
			}
			return "", "", fmt.Errorf("authorization was refused: %s", desc)
		}
		code, state = q.Get("code"), q.Get("state")
	} else {
		code = input
	}

	// "code#state" — split whichever field carries it.
	if i := strings.Index(code, "#"); i >= 0 {
		if state == "" {
			state = code[i+1:]
		}
		code = code[:i]
	}

	if code == "" {
		return "", "", fmt.Errorf("no authorization code found in %q", input)
	}
	return code, state, nil
}

// Result is one successful token exchange.
type Result struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	// RefreshTokenExpiresAt is when re-authorisation becomes unavoidable —
	// refreshing cannot extend it. Zero when the provider did not say.
	//
	// This is the difference between an account that lapses silently and one
	// that warns first. Refreshing keeps the access token alive indefinitely,
	// right up until this passes, at which point every refresh fails and the
	// only fix is a human at a browser.
	RefreshTokenExpiresAt time.Time
	// Email and AccountUUID are populated on the initial exchange. A refresh
	// may omit them entirely, which is why callers must never use them to
	// re-identify an existing account.
	Email       string
	AccountUUID string
}
