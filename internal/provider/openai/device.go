package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"claudication/internal/oauth"
)

// Signing in with a device code, as `codex login --device-auth` does it
// (Codex main, login/src/device_code_auth.rs; crush has no device flow).
//
//	POST {issuer}/api/accounts/deviceauth/usercode   {"client_id"}
//	  → {"device_auth_id", "user_code", "interval"}  interval a string of seconds
//	the operator opens https://auth.openai.com/codex/device and types user_code
//	POST {issuer}/api/accounts/deviceauth/token      {"device_auth_id","user_code"}
//	  → 403 or 404 while nobody has approved it
//	  → {"authorization_code", "code_verifier"}      once someone has
//	then the ordinary code exchange, with redirect_uri {issuer}/deviceauth/callback
//
// Fifteen minutes to approve. Measured end to end with scripts/probe-chatgpt.py
// on 2026-10-19, on a Plus account that had changed no security setting: no
// per-account opt-in was asked for. Whether one is ever asked for — a
// workspace with stricter policy — is not known; the operator would see the
// endpoint's own refusal.
//
// Why this and not a redirect to paste back: nothing has to reach the gateway
// from the operator's browser, so it works the same for a gateway on another
// machine. Codex's browser flow redirects to a listener on 127.0.0.1:1455,
// which on a remote gateway is nobody, and which a local Codex mid-login would
// answer first.
const (
	deviceCodeURL   = issuer + "/api/accounts/deviceauth/usercode"
	deviceTokenURL  = issuer + "/api/accounts/deviceauth/token"
	deviceRedirect  = issuer + "/deviceauth/callback"
	VerificationURI = issuer + "/codex/device"
	deviceLifetime  = 15 * time.Minute
	// defaultInterval is Codex's when the endpoint names none.
	defaultInterval = 5 * time.Second
)

// StartDevice asks for a code to show the operator.
func StartDevice(ctx context.Context, client *http.Client) (oauth.Device, error) {
	raw, status, err := postJSON(ctx, client, deviceCodeURL, map[string]string{"client_id": ClientID})
	if err != nil {
		return oauth.Device{}, err
	}
	if status != http.StatusOK {
		return oauth.Device{}, fmt.Errorf("the device-code endpoint returned %d: %s", status, clip(string(raw), 300))
	}
	var out struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		Interval     json.RawMessage `json:"interval"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return oauth.Device{}, fmt.Errorf("read the device code: %w", err)
	}
	if out.DeviceAuthID == "" || out.UserCode == "" {
		return oauth.Device{}, errors.New("the device-code endpoint answered without a code")
	}
	return oauth.Device{
		ID:              out.DeviceAuthID,
		UserCode:        out.UserCode,
		VerificationURI: VerificationURI,
		Interval:        interval(out.Interval),
		ExpiresAt:       time.Now().Add(deviceLifetime),
	}, nil
}

// interval reads the endpoint's polling interval, which Codex receives as a
// string of seconds; a number is taken too, and anything else is the default.
func interval(raw json.RawMessage) time.Duration {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 60 {
		return time.Duration(n) * time.Second
	}
	return defaultInterval
}

// PollDevice asks once whether the code has been approved, and when it has,
// redeems it. pending is true while nobody has; the caller asks again after
// the code's interval.
func PollDevice(ctx context.Context, client *http.Client, d oauth.Device) (oauth.Result, bool, error) {
	if !d.ExpiresAt.IsZero() && time.Now().After(d.ExpiresAt) {
		return oauth.Result{}, false, fmt.Errorf("%w: the code expired before it was approved", oauth.ErrDeviceSpent)
	}
	raw, status, err := postJSON(ctx, client, deviceTokenURL, map[string]string{
		"device_auth_id": d.ID,
		"user_code":      d.UserCode,
	})
	if err != nil {
		return oauth.Result{}, false, err
	}
	switch status {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusNotFound:
		// Codex's reading: not approved yet.
		return oauth.Result{}, true, nil
	default:
		return oauth.Result{}, false, fmt.Errorf("the device-code endpoint returned %d: %s", status, clip(string(raw), 300))
	}
	var got struct {
		AuthorizationCode string `json:"authorization_code"`
		CodeVerifier      string `json:"code_verifier"`
	}
	if err := json.Unmarshal(raw, &got); err != nil || got.AuthorizationCode == "" {
		return oauth.Result{}, false, fmt.Errorf("%w: the code was approved but no authorization code came with it",
			oauth.ErrDeviceSpent)
	}
	// Past here the approval is spent: the endpoint has handed its code over,
	// and asked again it answers as for a code nobody approved. So a failed
	// exchange ends the attempt rather than leaving it to be asked about
	// until it expires.
	res, err := exchangeCode(ctx, client, got.AuthorizationCode, got.CodeVerifier, deviceRedirect)
	if err != nil {
		return oauth.Result{}, false, fmt.Errorf("%w: the code was approved but could not be redeemed: %v",
			oauth.ErrDeviceSpent, err)
	}
	return res, false, nil
}

func postJSON(ctx context.Context, client *http.Client, url string, body any) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, tokenTimeout)
	defer cancel()
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("contact %s: %w", issuer, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, 0, err
	}
	return raw, resp.StatusCode, nil
}
