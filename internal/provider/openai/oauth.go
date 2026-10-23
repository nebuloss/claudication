package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"claudication/internal/oauth"
)

// "Sign in with ChatGPT", as Codex does it.
//
// Codex's own OAuth client. Like Anthropic's it is not a documented public
// API, and any of it can change without notice; this file is the only place
// it appears. Facts from Codex main (codex-rs/login, read 2026-10-19) and
// crush (internal/oauth/openai), which agree except where noted in doc.go.
// ClientID is Codex's, encoded (see oauth.Reveal). Codex:
// login/src/auth/manager.rs CLIENT_ID; crush: oauth.go.
var ClientID = oauth.Reveal("bm5hcmg3cFhhWGtDMGYzN1pFRW1hb01FX3BwYQ==")

const (
	issuer   = "https://auth.openai.com"
	tokenURL = issuer + "/oauth/token"

	// revokeURL is Codex's REVOKE_TOKEN_URL (login/src/auth/manager.rs).
	revokeURL = issuer + "/oauth/revoke"
)

// tokenTimeout bounds one call to the token endpoint.
const tokenTimeout = 30 * time.Second

// tokenResponse is what the token endpoint answers, for a code and for a
// refresh alike. A refresh may leave out refresh_token and id_token.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// exchangeCode redeems an authorization code. Form-encoded, as Codex and crush
// both send it and as scripts/probe-chatgpt.py redeemed one on 2026-10-19.
func exchangeCode(ctx context.Context, client *http.Client, code, verifier, redirect string) (oauth.Result, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {ClientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirect},
	}
	tr, err := postToken(ctx, client, "application/x-www-form-urlencoded", []byte(form.Encode()))
	if err != nil {
		return oauth.Result{}, err
	}
	res := tr.result()
	c := claimsOf(tr.IDToken)
	if c.accountID() == "" {
		// crush falls back to the access token for a server that sends no
		// id_token; the account id is the one thing a request cannot do
		// without.
		if ac := claimsOf(tr.AccessToken); ac.accountID() != "" {
			c.ChatGPTAccountID = ac.accountID()
		}
	}
	res.Email = c.email()
	res.AccountUUID = c.accountID()
	res.Plan = c.plan()
	if res.AccountUUID == "" {
		return oauth.Result{}, errors.New("the sign-in named no ChatGPT account; the backend cannot be called without one")
	}
	return res, nil
}

// Refresh trades a refresh token for new tokens.
//
// JSON, as Codex sends it (manager.rs request_chatgpt_token_refresh), no
// scope; crush sends a form and is evidently accepted too. The answer may
// leave the refresh token out, in which case the old one is still good and is
// kept. Identity is never taken from a refresh.
func Refresh(ctx context.Context, client *http.Client, refreshToken string) (oauth.Result, error) {
	body, err := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     ClientID,
		"refresh_token": refreshToken,
	})
	if err != nil {
		return oauth.Result{}, err
	}
	tr, err := postToken(ctx, client, "application/json", body)
	if err != nil {
		return oauth.Result{}, err
	}
	res := tr.result()
	if res.RefreshToken == "" {
		res.RefreshToken = refreshToken
	}
	return res, nil
}

// result is the tokens, with when the access token expires: expires_in when
// the endpoint says, else the token's own exp claim, else an hour — which is
// what was measured (exp − now = 3453 s straight after a sign-in).
func (tr tokenResponse) result() oauth.Result {
	res := oauth.Result{AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken}
	switch {
	case tr.ExpiresIn > 0:
		res.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	case claimsOf(tr.AccessToken).Exp > 0:
		res.ExpiresAt = time.Unix(claimsOf(tr.AccessToken).Exp, 0)
	default:
		res.ExpiresAt = time.Now().Add(time.Hour)
	}
	return res
}

// permanentRefusals are the token endpoint's answers that no retry fixes, as
// Codex classifies them (manager.rs classify_refresh_token_failure).
var permanentRefusals = []string{
	"refresh_token_expired", "refresh_token_reused", "refresh_token_invalidated", "invalid_grant",
}

func postToken(ctx context.Context, client *http.Client, contentType string, body []byte) (tokenResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, tokenTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(string(body)))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")

	// Never follow a redirect on a token exchange: a 30x turns the POST into
	// a body-less GET, which the endpoint then refuses as a request with no
	// fields — the failure Anthropic's client taught us to look for.
	noFollow := *client
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := noFollow.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("contact the OpenAI token endpoint: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("read the token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("token endpoint returned %d: %s", resp.StatusCode, clip(strings.TrimSpace(string(raw)), 400))
		// Any 401 is permanent too: Codex treats it so, and a refresh token
		// the endpoint will not authenticate will not start to.
		if resp.StatusCode == http.StatusUnauthorized {
			return tokenResponse{}, fmt.Errorf("%w: %s", oauth.ErrInvalidGrant, msg)
		}
		for _, code := range permanentRefusals {
			if strings.Contains(string(raw), code) {
				return tokenResponse{}, fmt.Errorf("%w: %s", oauth.ErrInvalidGrant, msg)
			}
		}
		return tokenResponse{}, errors.New(msg)
	}
	var tr tokenResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return tokenResponse{}, fmt.Errorf("decode the token response: %w", err)
	}
	if tr.AccessToken == "" {
		return tokenResponse{}, errors.New("the token response carried no access_token")
	}
	return tr, nil
}

// claims is what the gateway reads out of a token's payload, unverified: it
// is the identity the operator just signed in as, read to name the account,
// never to authorise anything. Measured 2026-10-19 (testdata/identity.json):
// email, name and sub at the top level; chatgpt_account_id,
// chatgpt_plan_type, chatgpt_user_id and the subscription's dates under
// https://api.openai.com/auth. Codex reads the same (login/src/token_data.rs
// parse_chatgpt_jwt_claims); crush also accepts chatgpt_account_id at the top
// level, so both are read.
type claims struct {
	Email            string `json:"email"`
	Exp              int64  `json:"exp"`
	ChatGPTAccountID string `json:"chatgpt_account_id"`
	Profile          *struct {
		Email string `json:"email"`
	} `json:"https://api.openai.com/profile"`
	Auth *struct {
		ChatGPTAccountID string `json:"chatgpt_account_id"`
		ChatGPTPlanType  string `json:"chatgpt_plan_type"`
	} `json:"https://api.openai.com/auth"`
}

func (c claims) email() string {
	if c.Email != "" {
		return c.Email
	}
	if c.Profile != nil {
		return c.Profile.Email
	}
	return ""
}

func (c claims) accountID() string {
	if c.ChatGPTAccountID != "" {
		return c.ChatGPTAccountID
	}
	if c.Auth != nil {
		return c.Auth.ChatGPTAccountID
	}
	return ""
}

func (c claims) plan() string {
	if c.Auth != nil {
		return c.Auth.ChatGPTPlanType
	}
	return ""
}

// claimsOf reads a JWT's payload without checking its signature. Anything
// that is not a JWT yields no claims rather than an error: a missing claim is
// answered by the caller.
func claimsOf(token string) claims {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return claims{}
	}
	var c claims
	_ = json.Unmarshal(payload, &c)
	return c
}

// revokeTimeout matches Anthropic's: nothing waits on the answer.
const revokeTimeout = 5 * time.Second

// Revoke hands a refresh token back. Codex names the URL; the body is the
// standard one (RFC 7009, form-encoded), which is not measured — revoking is
// best effort, and a failure is logged by the caller and nothing more.
func Revoke(ctx context.Context, client *http.Client, refreshToken string) error {
	if refreshToken == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, revokeTimeout)
	defer cancel()
	form := url.Values{
		"token":           {refreshToken},
		"token_type_hint": {"refresh_token"},
		"client_id":       {ClientID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, revokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("revoke token: %w", err)
	}
	defer resp.Body.Close()
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("revoke token: upstream returned %d: %s",
			resp.StatusCode, clip(strings.TrimSpace(string(detail)), 200))
	}
	return nil
}

// clip shortens an upstream's text for an error message, marking the cut.
func clip(s string, max int) string {
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
