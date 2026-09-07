package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Revocation, as the client's own /logout performs it.
//
// Reversed from the client bundle (v2.1.263), where revokeOAuthToken does:
//
//	POST https://platform.claude.com/v1/oauth/token/revoke
//	Content-Type: application/json, no Authorization header
//	{"token": <refresh token>, "token_type_hint": "refresh_token",
//	 "client_id": <the client id the token was issued to>}
//	timeout 5000, no retries, failure swallowed and logout continues
//
// Without this, removing an account only made the gateway forget a credential
// that stayed live upstream until it aged out. Deleting an account should
// actually hand it back.
const anthropicRevokeURL = "https://platform.claude.com/v1/oauth/token/revoke"

// revokeTimeout matches the client's own 5s budget. Nothing waits on the
// answer, so a slow endpoint must not hold up the delete that triggered it.
const revokeTimeout = 5 * time.Second

// RevokeAnthropic asks the provider to invalidate a refresh token.
//
// Note the host: revocation goes to platform.claude.com, which is where the
// client sends it, and not to the api.anthropic.com token endpoint this
// package uses for the exchange. The two were chosen on different evidence —
// the exchange host is the one that could be shown to parse our body, and this
// one is the one the client actually calls. They are allowed to differ.
//
// Only the refresh token is revoked. The client never sends the access token
// either: it is short-lived and left to expire.
func RevokeAnthropic(ctx context.Context, client *http.Client, refreshToken, clientID string) error {
	return revokeAt(ctx, client, anthropicRevokeURL, refreshToken, clientID)
}

// revokeAt is the same call against a caller-chosen URL, so the request shape
// can be tested without reaching the real provider.
func revokeAt(ctx context.Context, client *http.Client, url, refreshToken, clientID string) error {
	if refreshToken == "" {
		return nil
	}
	if clientID == "" {
		clientID = AnthropicClientID
	}

	ctx, cancel := context.WithTimeout(ctx, revokeTimeout)
	defer cancel()

	body, err := json.Marshal(map[string]string{
		"token":           refreshToken,
		"token_type_hint": "refresh_token",
		"client_id":       clientID,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	// No Authorization header: the token being revoked is the credential, and
	// the client sends nothing else.
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("revoke token: %w", err)
	}
	defer resp.Body.Close()

	// The response body is not read for meaning. The client never inspects it
	// either, and nothing in the bundle describes its shape — so the status is
	// all there is to go on, and even that only decides what gets logged.
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("revoke token: upstream returned %d: %s",
			resp.StatusCode, trimDetail(detail))
	}
	return nil
}

func trimDetail(b []byte) string {
	const max = 200
	s := string(bytes.TrimSpace(b))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
