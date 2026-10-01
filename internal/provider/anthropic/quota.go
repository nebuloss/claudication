package anthropic

import (
	"encoding/json"
	"time"

	"claudication/internal/store"
)

// Quota is the usage figures as the store keeps them: what the pool routes on
// and the accounts screen shows.
//
// A window the endpoint did not report is -1, not zero — an account nobody
// has asked about is unknown, not idle, and the two should not route the same
// way. The limits array is passed through untouched: it is the server's own
// normalised view, it is what the client renders, and re-deriving it here
// would only be a second thing to keep in step with the upstream.
func (u AccountUsage) Quota() store.AccountQuota {
	q := store.AccountQuota{UpdatedAt: u.FetchedAt}
	if w := u.FiveHour; w != nil {
		q.FiveHourUtil = w.Utilization
		q.FiveHourReset = parseUsageTime(w.ResetsAt)
		q.FiveHourStatus = lockedOrAllowed(w.LockedReason)
	} else {
		q.FiveHourUtil = -1
	}
	if w := u.SevenDay; w != nil {
		q.SevenDayUtil = w.Utilization
		q.SevenDayReset = parseUsageTime(w.ResetsAt)
		q.SevenDayStatus = lockedOrAllowed(w.LockedReason)
	} else {
		q.SevenDayUtil = -1
	}
	if raw, err := json.Marshal(u.Limits); err == nil {
		q.Detail = string(raw)
	}
	return q
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
