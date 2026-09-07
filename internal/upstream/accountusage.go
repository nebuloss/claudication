package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// The subscription usage endpoint, as the Claude Code client calls it.
//
// Reversed out of the client bundle (v2.1.263), where fetchUtilization does:
//
//	GET /api/oauth/usage           (or ?at_wall=1&skip_spend=1 at the limit)
//	timeout 5000, Content-Type: application/json, OAuth credentials,
//	refreshing the token once on a 401 and retrying
//
// This is what `/usage` prints. It is strictly better than reading the
// rate-limit headers off a relayed response: it answers before the account has
// served anything, it carries the per-model weekly limits, and it is the same
// number the operator sees in their own client.
const usagePath = "/api/oauth/usage"

// usageTimeout matches the client's own 5s budget. This is a status call; if
// it is slow, the answer is to show the previous figure, not to wait.
const usageTimeout = 5 * time.Second

// AccountUsage is the /api/oauth/usage response.
//
// Only the fields worth showing are named. The response also carries a dozen
// codenamed buckets (tangelo, nimbus_quill, juniper_tide…) that are empty on
// an ordinary plan; Limits is the server's own normalised view and is what the
// client renders, so that is what this follows.
type AccountUsage struct {
	FiveHour   *UsageWindow `json:"five_hour"`
	SevenDay   *UsageWindow `json:"seven_day"`
	Limits     []UsageLimit `json:"limits"`
	ExtraUsage *ExtraUsage  `json:"extra_usage"`

	// FetchedAt is set by the caller, not the server.
	FetchedAt time.Time `json:"-"`
}

// UsageWindow is one rolling limit. Utilization is a percentage, 0-100 — note
// that the response headers report the same quantity as a 0-1 fraction.
type UsageWindow struct {
	Utilization  float64 `json:"utilization"`
	ResetsAt     *string `json:"resets_at"`
	LockedReason *string `json:"locked_reason"`
}

// UsageLimit is one row of the client's own usage display.
//
// Kind is "session", "weekly_all" or "weekly_scoped"; a scoped row names the
// model it applies to. Severity is "normal" or a warning/rejection.
type UsageLimit struct {
	Kind     string  `json:"kind"`
	Group    string  `json:"group"`
	Percent  float64 `json:"percent"`
	Severity string  `json:"severity"`
	ResetsAt *string `json:"resets_at"`
	IsActive bool    `json:"is_active"`
	Scope    *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

// Title renders the row the way the client labels it: "Current session",
// "Current week (all models)", "Current week (Fable)".
func (l UsageLimit) Title() string {
	switch l.Kind {
	case "session":
		return "Current session"
	case "weekly_all":
		return "Current week (all models)"
	case "weekly_scoped":
		if l.Scope != nil && l.Scope.Model != nil && l.Scope.Model.DisplayName != "" {
			return "Current week (" + l.Scope.Model.DisplayName + ")"
		}
		return "Current week (scoped)"
	}
	return l.Kind
}

// ExtraUsage is the pay-as-you-go overflow, off on most plans.
type ExtraUsage struct {
	IsEnabled         bool     `json:"is_enabled"`
	Utilization       *float64 `json:"utilization"`
	SpendLimitReached bool     `json:"spend_limit_reached"`
}

// Utilization is the binding figure across the windows the server reported,
// as a percentage. -1 when the response named none.
func (u AccountUsage) Utilization() float64 {
	worst := -1.0
	for _, w := range []*UsageWindow{u.FiveHour, u.SevenDay} {
		if w != nil && w.Utilization > worst {
			worst = w.Utilization
		}
	}
	// A scoped weekly limit can bind before either headline window does.
	for _, l := range u.Limits {
		if l.Percent > worst {
			worst = l.Percent
		}
	}
	return worst
}

// Locked reports whether the subscription is currently refusing work, which
// the server says by naming a reason rather than by a status field.
func (u AccountUsage) Locked() bool {
	for _, w := range []*UsageWindow{u.FiveHour, u.SevenDay} {
		if w != nil && w.LockedReason != nil && *w.LockedReason != "" {
			return true
		}
	}
	return false
}

// FetchUsage asks the upstream what this account has spent.
//
// The caller supplies a token that is already fresh; unlike the client, this
// does not refresh-and-retry here, because the pool has already done that and
// a second refresh would race the first over a rotating refresh token.
func FetchUsage(ctx context.Context, client *http.Client, accessToken string) (AccountUsage, error) {
	ctx, cancel := context.WithTimeout(ctx, usageTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, AnthropicBaseURL+usagePath, nil)
	if err != nil {
		return AccountUsage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")

	resp, err := client.Do(req)
	if err != nil {
		return AccountUsage{}, fmt.Errorf("fetch usage: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return AccountUsage{}, fmt.Errorf("read usage: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return AccountUsage{}, fmt.Errorf("usage endpoint returned %d: %s",
			resp.StatusCode, trimForError(body))
	}

	var out AccountUsage
	if err := json.Unmarshal(body, &out); err != nil {
		return AccountUsage{}, fmt.Errorf("parse usage: %w", err)
	}
	out.FetchedAt = time.Now().UTC()
	return out, nil
}

func trimForError(b []byte) string {
	const max = 200
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}
