package anthropic

import (
	"encoding/json"
	"time"

	"claudication/internal/provider"
	"claudication/internal/store"
)

// Usage is the usage figures as every provider reports them: the windows by
// their length, the limits array as the detail.
//
// A window the endpoint did not report is left out, not reported as zero — an
// account nobody has asked about is unknown, not idle, and the two should not
// route the same way. So is the 5-hour window of a plan that has none: in
// November 2026 Anthropic removed the session limit from some subscriptions,
// and their usage then lists no "session" row among its limits, which is the
// server's own normalised view and what the client renders — whatever the
// older five_hour field still says. The limits array is passed through untouched: it is the
// server's own normalised view, it is what the client renders, and
// re-deriving it here would only be a second thing to keep in step with the
// upstream.
func (u AccountUsage) Usage() provider.Quota {
	q := provider.Quota{At: u.FetchedAt}
	for _, w := range []struct {
		seconds int64
		win     *UsageWindow
	}{{store.FiveHourSeconds, u.FiveHour}, {store.SevenDaySeconds, u.SevenDay}} {
		if w.win == nil || (w.seconds == store.FiveHourSeconds && !u.hasSession()) {
			continue
		}
		q.Windows = append(q.Windows, provider.Window{
			Seconds:     w.seconds,
			UsedPercent: w.win.Utilization,
			ResetAt:     parseUsageTime(w.win.ResetsAt),
			Status:      lockedOrAllowed(w.win.LockedReason),
		})
	}
	if raw, err := json.Marshal(u.Limits); err == nil {
		q.Detail = string(raw)
	}
	return q
}

// hasSession reports whether the plan has a session limit: a "session" row
// among the limits, or no limits sent at all — a response from before the
// rows existed, read as it always was.
func (u AccountUsage) hasSession() bool {
	if len(u.Limits) == 0 {
		return true
	}
	for _, l := range u.Limits {
		if l.Kind == "session" {
			return true
		}
	}
	return false
}

func parseUsageTime(v *string) time.Time {
	if v == nil || *v == "" {
		return time.Time{}
	}
	// The endpoint sends RFC3339 with a fractional second and a numeric zone.
	t, err := time.Parse(time.RFC3339, *v)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// lockedOrAllowed maps the endpoint's locked_reason onto the status vocabulary
// the rest of the gateway already speaks.
func lockedOrAllowed(reason *string) string {
	if reason != nil && *reason != "" {
		return *reason
	}
	return "allowed"
}

// limitRows reads a stored limits array back as the accounts screen's rows,
// titled the way the client titles them so the two read the same.
func limitRows(detail string) []provider.Limit {
	if detail == "" {
		return nil
	}
	var rows []UsageLimit
	if err := json.Unmarshal([]byte(detail), &rows); err != nil {
		return nil
	}
	out := make([]provider.Limit, 0, len(rows))
	for _, l := range rows {
		row := provider.Limit{
			Title:    l.Title(),
			Kind:     l.Kind,
			Percent:  l.Percent,
			Severity: l.Severity,
			IsActive: l.IsActive,
		}
		if l.ResetsAt != nil {
			row.ResetsAt = *l.ResetsAt
		}
		out = append(out, row)
	}
	return out
}
