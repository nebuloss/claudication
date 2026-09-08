// Package oauth implements the provider login flows.
//
// The Anthropic constants below are Claude Code's own OAuth client. They are
// not a documented public API — Anthropic's published authentication methods
// are API keys, Workload Identity Federation and App Attest, none of which
// relay a claude.ai subscription. Every value here was verified against the
// Claude Code 2.1.263 binary, and any of them can change without notice, so
// this file is deliberately the only place they appear.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrInvalidGrant is the refusal that never comes right on its own: the
// refresh token has been revoked, has expired, or was already spent. Retrying
// it is not patience, it is a loop — only a human at a browser can fix it.
var ErrInvalidGrant = errors.New("the refresh token is no longer valid")

const (
	AnthropicClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

	// Captured from `claude auth login --claudeai` on 2.1.263. Not claude.ai:
	// that host answers "client_id: Field required" for this same request,
	// even though it is where claude.com/cai appears to redirect. auth2api
	// still uses claude.ai and is simply out of date.
	anthropicAuthURL = "https://claude.com/cai/oauth/authorize"

	// Deliberately NOT the bundle's TOKEN_URL
	// (https://platform.claude.com/v1/oauth/token). That host was tried and
	// answered "client_id: Field required" for a request carrying a client_id,
	// and it rate-limits hard enough that its behaviour on a well-formed body
	// could never be observed. This host was verified by probe to parse our
	// exact JSON body — a fake code gets the correct
	// {"error":"invalid_grant","error_description":"Invalid 'code' in request."}
	// rather than a schema complaint. Prefer the endpoint we can demonstrate
	// works; revisit if it is retired.
	anthropicTokenURL = "https://api.anthropic.com/v1/oauth/token"

	// The client's MANUAL_REDIRECT_URL: Anthropic renders a page showing the
	// code, which is the flow `claude` itself uses when it asks you to paste
	// one. The client also builds a loopback variant for its own local
	// listener, but we have nothing listening on the operator's machine, so
	// that form only ever produced a connection error to copy out of the
	// address bar. This one is confirmed working end to end.
	RedirectManual = "https://platform.claude.com/oauth/code/callback"

	// The client's full default scope set, in its own order. Requesting a
	// subset is what the consent screen rejects as "Invalid request format" —
	// notably user:sessions:claude_code, which the claude.ai flow expects.
	//
	// Built in the client as the deduplicated union of
	//   [org:create_api_key, user:profile]
	// and
	//   [user:profile, user:inference, user:sessions:claude_code,
	//    user:mcp_servers, user:file_upload]
	anthropicScopes = "org:create_api_key user:profile user:inference " +
		"user:sessions:claude_code user:mcp_servers user:file_upload"
)

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

// AnthropicAuthURL builds the URL the operator opens in a browser, for the
// given redirect.
//
// The shape is copied from a URL captured out of `claude auth login
// --claudeai`, which is the only reference that has been observed working:
//
//	https://claude.com/cai/oauth/authorize?code=true&client_id=…&response_type=code
//	&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback
//	&scope=org%3Acreate_api_key+user%3Aprofile+…
//	&code_challenge=…&code_challenge_method=S256&state=…
//
// Two details are load-bearing and were each got wrong by following auth2api
// instead: scope sits fifth, before code_challenge, and its colons are
// percent-encoded. url.QueryEscape reproduces both — "%3A" for the colons and
// "+" for the separators — so the whole scope string goes through it as one
// value. Parameters are emitted in order rather than via url.Values.Encode,
// which sorts alphabetically.
func AnthropicAuthURL(state string, pkce PKCE, redirectURI string) string {
	params := [][2]string{
		{"code", "true"},
		{"client_id", AnthropicClientID},
		{"response_type", "code"},
		{"redirect_uri", redirectURI},
		{"scope", anthropicScopes},
		{"code_challenge", pkce.Challenge},
		{"code_challenge_method", "S256"},
		{"state", state},
	}

	var q strings.Builder
	for i, p := range params {
		if i > 0 {
			q.WriteByte('&')
		}
		q.WriteString(url.QueryEscape(p[0]))
		q.WriteByte('=')
		q.WriteString(url.QueryEscape(p[1]))
	}
	return anthropicAuthURL + "?" + q.String()
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

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	// RefreshTokenExpiresIn is the lifetime of the refresh token itself, in
	// seconds. It is the field behind Claude Code's "Your login expires in N
	// days · run /login to renew" banner. A pointer because it is optional:
	// absent means the provider did not say, which is not the same as zero.
	RefreshTokenExpiresIn *int64 `json:"refresh_token_expires_in"`
	Account               struct {
		EmailAddress string `json:"email_address"`
		UUID         string `json:"uuid"`
	} `json:"account"`
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

func postAnthropicToken(ctx context.Context, client *http.Client, body any) (Result, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return Result{}, fmt.Errorf("encode token request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicTokenURL, strings.NewReader(string(payload)))
	if err != nil {
		return Result{}, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Never follow a redirect on a token exchange.
	//
	// Go's default client follows one, and on a 301/302/303 it rewrites the
	// POST into a GET and drops the body. The endpoint then sees no fields at
	// all and rejects the request naming the first required one — reading as
	// "client_id: Field required" for a request that plainly contained a
	// client_id. curl hides this too, since it does not follow redirects
	// unless asked. Refusing outright turns a silent, badly-misattributed
	// failure into an explicit one.
	noFollow := *client
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	resp, err := noFollow.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("contact the Anthropic token endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return Result{}, fmt.Errorf(
			"token endpoint redirected (%d) to %q; the request body would have been dropped",
			resp.StatusCode, resp.Header.Get("Location"))
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Result{}, fmt.Errorf("read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// Pass the upstream's own words through: they are the only useful
		// signal when a code has expired or a refresh token was reused. The
		// request id travels with them, since that is what Anthropic's logs
		// key off.
		msg := fmt.Sprintf("token endpoint returned %d: %s",
			resp.StatusCode, strings.TrimSpace(string(raw)))
		if id := resp.Header.Get("request-id"); id != "" {
			msg += " (request-id " + id + ")"
		}
		// invalid_grant is the one refusal that will never come right on its
		// own: the refresh token has been revoked, expired, or already spent.
		// Distinguishing it is what lets a caller stop retrying something no
		// amount of waiting will fix — the client keeps a set of these and
		// refuses to present them again.
		var oe struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &oe) == nil && oe.Error == "invalid_grant" {
			return Result{}, fmt.Errorf("%w: %s", ErrInvalidGrant, msg)
		}
		return Result{}, errors.New(msg)
	}

	var tr tokenResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return Result{}, fmt.Errorf("decode token response: %w", err)
	}
	if tr.AccessToken == "" {
		return Result{}, fmt.Errorf("token response contained no access_token")
	}

	expiresIn := tr.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	res := Result{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(expiresIn) * time.Second),
		Email:        tr.Account.EmailAddress,
		AccountUUID:  tr.Account.UUID,
	}
	if tr.RefreshTokenExpiresIn != nil && *tr.RefreshTokenExpiresIn > 0 {
		res.RefreshTokenExpiresAt = time.Now().Add(time.Duration(*tr.RefreshTokenExpiresIn) * time.Second)
	}
	return res, nil
}

// ExchangeAnthropicCode trades an authorization code for tokens.
//
// redirectURI must be the same one the authorize request carried. That much is
// a hard rule — the client replays its own choice here for exactly this reason
// (`useManualRedirect: !hasPendingResponse()`), and a mismatch is rejected.
func ExchangeAnthropicCode(ctx context.Context, client *http.Client, code, verifier, state, redirectURI string) (Result, error) {
	return postAnthropicToken(ctx, client, map[string]string{
		"code":          code,
		"grant_type":    "authorization_code",
		"client_id":     AnthropicClientID,
		"redirect_uri":  redirectURI,
		"code_verifier": verifier,
		"state":         state,
	})
}

// RefreshAnthropic exchanges a refresh token for a new access token.
func RefreshAnthropic(ctx context.Context, client *http.Client, refreshToken string) (Result, error) {
	res, err := postAnthropicToken(ctx, client, map[string]string{
		"client_id":     AnthropicClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
	})
	if err != nil {
		return Result{}, err
	}
	// Some refreshes return no new refresh token; keep using the old one
	// rather than storing an empty string and locking the account out.
	if res.RefreshToken == "" {
		res.RefreshToken = refreshToken
	}
	return res, nil
}
