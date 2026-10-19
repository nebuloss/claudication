package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"claudication/internal/provider"
)

// The subscription usage endpoint, as Codex polls it
// (backend-client/src/client/rate_limit_resets.rs):
//
//	GET https://chatgpt.com/backend-api/wham/usage
//	Authorization: Bearer <access>, ChatGPT-Account-Id: <account>
//
// Measured 2026-10-19 (testdata/usage.json, a Plus account):
//
//	plan_type                      "plus"
//	rate_limit.allowed             true
//	rate_limit.limit_reached       false
//	rate_limit.primary_window      {used_percent, limit_window_seconds: 18000,
//	                                reset_after_seconds, reset_at}
//	rate_limit.secondary_window    the same, limit_window_seconds: 604800
//	additional_rate_limits         null
//	credits, model_usage, spend_control, promo, …
//
// The relayed answers carry the same windows in their headers (limits.go),
// current to the request; this is what an account that has served nothing is
// known by, and what the poller asks.
const usageURL = backendURL + "/wham/usage"

// usageTimeout is Anthropic's: a status call, where slow means show the last
// figure.
const usageTimeout = 5 * time.Second

type usageWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

type rateLimit struct {
	Allowed         *bool        `json:"allowed"`
	LimitReached    bool         `json:"limit_reached"`
	PrimaryWindow   *usageWindow `json:"primary_window"`
	SecondaryWindow *usageWindow `json:"secondary_window"`
}

// usageResponse is the endpoint's answer, in the fields read.
type usageResponse struct {
	PlanType  string    `json:"plan_type"`
	RateLimit rateLimit `json:"rate_limit"`
	// AdditionalRateLimits are limits beside the plan's own — null on the
	// account measured; shaped per Codex's
	// codex-backend-openapi-models/src/models/rate_limit_*.
	AdditionalRateLimits []struct {
		LimitName string    `json:"limit_name"`
		RateLimit rateLimit `json:"rate_limit"`
	} `json:"additional_rate_limits"`
}

// quota is the answer as the windows every provider reports. The plan's own
// two windows decide whether the account may serve; an additional limit is
// shown in the detail and does not, because what it limits is not said.
func (u usageResponse) quota() provider.Quota {
	rows := windowRows("", u.RateLimit)
	for _, extra := range u.AdditionalRateLimits {
		rows = append(rows, windowRows(extra.LimitName, extra.RateLimit)...)
	}
	q := quotaFromRows(rows)
	// The plan's windows are the first rows; only those reach the columns.
	q.Windows = q.Windows[:len(windowRows("", u.RateLimit))]
	if u.RateLimit.LimitReached || (u.RateLimit.Allowed != nil && !*u.RateLimit.Allowed) {
		q.Limited = true
	}
	return q
}

func windowRows(name string, rl rateLimit) []windowRow {
	var rows []windowRow
	for i, w := range []*usageWindow{rl.PrimaryWindow, rl.SecondaryWindow} {
		if w == nil || w.LimitWindowSeconds <= 0 {
			continue
		}
		label := name
		if label == "" {
			label = windowNames[i]
		}
		row := windowRow{Name: label, WindowSeconds: w.LimitWindowSeconds, UsedPercent: w.UsedPercent, ResetAt: w.ResetAt}
		if row.ResetAt <= 0 && w.ResetAfterSeconds > 0 {
			row.ResetAt = time.Now().Add(time.Duration(w.ResetAfterSeconds) * time.Second).Unix()
		}
		rows = append(rows, row)
	}
	return rows
}

// FetchUsage asks what an account has spent. The token is already fresh: the
// pool refreshed it, and a second refresh here would race the first.
func FetchUsage(ctx context.Context, client *http.Client, cred provider.Credential) (provider.Quota, error) {
	ctx, cancel := context.WithTimeout(ctx, usageTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, usageURL, nil)
	if err != nil {
		return provider.Quota{}, err
	}
	Provider{}.Authorize(req, cred)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return provider.Quota{}, fmt.Errorf("fetch usage: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return provider.Quota{}, fmt.Errorf("read usage: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return provider.Quota{}, fmt.Errorf("usage endpoint returned %d: %s", resp.StatusCode, clip(string(body), 200))
	}
	var u usageResponse
	if err := json.Unmarshal(body, &u); err != nil {
		return provider.Quota{}, fmt.Errorf("parse usage: %w", err)
	}
	return u.quota(), nil
}
