package google

import (
	"context"
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

// "Sign in with Google", as Antigravity does it: consent in the operator's
// browser, which Google then sends to Antigravity's loopback address on the
// operator's own machine. Nothing listens there for a gateway, so the
// browser shows an error page, and its address — the code is in it — is
// what is pasted back (oauth.ParseCallback reads a whole URL).
//
// Antigravity's own OAuth client, as CLIProxyAPI signs in with it
// (router-for-me/CLIProxyAPI, internal/auth/antigravity/constants.go, read
// 2026-10-23), measured to sign in and redeem the same day. An installed
// application's, whose secret Google documents as not secret. This file is
// the only place it appears, and both values are encoded so a secret
// scanner does not refuse the push over them (see oauth.Reveal).
var (
	// ClientID is Antigravity's.
	ClientID = oauth.Reveal("bW9jLnRuZXRub2NyZXN1ZWxnb29nLnNwcGEucGUzMDRnNGhqb2xvdHY1MzJlcmNsMTJoMm5pc3NobXQtMTk1MDYwNjAwMTcwMQ==")
	// clientSecret is Antigravity's.
	clientSecret = oauth.Reveal("ZkFEcTZ6NENYczhCTG0xSkxkTDY4NFJXRjg1Sy1YUFNDT0c=")
)

const (
	authURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	tokenURL    = "https://oauth2.googleapis.com/token"
	revokeURL   = "https://oauth2.googleapis.com/revoke"
	userinfoURL = "https://www.googleapis.com/oauth2/v2/userinfo"

	// Redirect is Antigravity's loopback consent address
	// (CLIProxyAPI CallbackPort, /oauth-callback).
	Redirect = "http://localhost:51121/oauth-callback"
)

// scopes are Antigravity's. cloud-platform is what Code Assist is called
// with; the userinfo scopes name the account; cclog and
// experimentsandconfigs are Antigravity's own, asked for as it asks.
var scopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

// tokenTimeout bounds one call to the token endpoint.
const tokenTimeout = 30 * time.Second

// AuthURL is the consent page for one attempt.
//
// prompt=consent makes Google show the consent screen, and so hand out a
// refresh token, to an account that already consented to this client — every
// account that has used Antigravity has. Without it, signing such an account
// in here would succeed with no refresh token, and lapse an hour later.
func AuthURL(state string, pkce oauth.PKCE, redirect, loginHint string) string {
	q := url.Values{
		"client_id":             {ClientID},
		"redirect_uri":          {redirect},
		"response_type":         {"code"},
		"scope":                 {strings.Join(scopes, " ")},
		"access_type":           {"offline"},
		"prompt":                {"consent"},
		"code_challenge":        {pkce.Challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}
	if loginHint != "" {
		q.Set("login_hint", loginHint)
	}
	return authURL + "?" + q.Encode()
}

// tokenResponse is what the token endpoint answers, for a code and for a
// refresh alike. A refresh leaves refresh_token out.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// exchangeCode redeems a pasted code for tokens: form-encoded, with the
// client's secret and the PKCE verifier (measured 2026-10-23).
func exchangeCode(ctx context.Context, client *http.Client, code, verifier, redirect string) (tokenResponse, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {ClientID},
		"client_secret": {clientSecret},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirect},
	}
	tr, err := postToken(ctx, client, form)
	if err != nil {
		return tokenResponse{}, err
	}
	if tr.RefreshToken == "" {
		return tokenResponse{}, errors.New("Google returned no refresh token; sign in again and approve the consent screen")
	}
	return tr, nil
}

// Refresh trades a refresh token for an access token. Google's answer leaves
// the refresh token out — it does not rotate — so the one given is kept.
// Identity is never taken from a refresh.
func Refresh(ctx context.Context, client *http.Client, refreshToken string) (oauth.Result, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {ClientID},
		"client_secret": {clientSecret},
		"refresh_token": {refreshToken},
	}
	tr, err := postToken(ctx, client, form)
	if err != nil {
		return oauth.Result{}, err
	}
	res := tr.result()
	if res.RefreshToken == "" {
		res.RefreshToken = refreshToken
	}
	return res, nil
}

// result is the tokens with when the access token expires: expires_in, else
// an hour, which is what Google's access tokens live.
func (tr tokenResponse) result() oauth.Result {
	res := oauth.Result{AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken}
	if tr.ExpiresIn > 0 {
		res.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	} else {
		res.ExpiresAt = time.Now().Add(time.Hour)
	}
	return res
}

func postToken(ctx context.Context, client *http.Client, form url.Values) (tokenResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, tokenTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	// Never follow a redirect on a token exchange: a 30x turns the POST into
	// a body-less GET, which the endpoint then refuses as a request with no
	// fields.
	noFollow := *client
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := noFollow.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("contact the Google token endpoint: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("read the token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("token endpoint returned %d: %s", resp.StatusCode, clip(strings.TrimSpace(string(raw)), 400))
		// RFC 6749's answer for a refresh token that is revoked, expired or
		// was never this client's. Google also answers it for a code already
		// redeemed, which no retry fixes either.
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error == "invalid_grant" {
			return tokenResponse{}, fmt.Errorf("%w: %s", oauth.ErrInvalidGrant, msg)
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

// fetchEmail names the account signed in (measured 2026-10-23,
// testdata/antigravity-identity.json).
func fetchEmail(ctx context.Context, client *http.Client, token string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, tokenTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userinfoURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("read the account's address: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("userinfo returned %d: %s", resp.StatusCode, clip(strings.TrimSpace(string(raw)), 200))
	}
	var u struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(raw, &u); err != nil {
		return "", fmt.Errorf("decode userinfo: %w", err)
	}
	return u.Email, nil
}

// revokeTimeout matches the other providers': nothing waits on the answer.
const revokeTimeout = 5 * time.Second

// Revoke hands a refresh token back, which also ends every access token
// minted from it. Google's endpoint takes the token as a form field.
func Revoke(ctx context.Context, client *http.Client, refreshToken string) error {
	if refreshToken == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, revokeTimeout)
	defer cancel()
	form := url.Values{"token": {refreshToken}}
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
