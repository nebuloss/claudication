package httpapi

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nebuloss/claudication/internal/store"
	"github.com/nebuloss/claudication/internal/upstream"
)

// usagePollInterval is how often every account's subscription usage is
// refreshed. The 5-hour window moves slowly and the endpoint is a status call,
// so this is about staying honest rather than about being live.
const usagePollInterval = 5 * time.Minute

// refreshAccountUsage asks the upstream what one account has spent and stores
// it. Returns the figures so a handler can answer with them directly.
func (s *Server) refreshAccountUsage(ctx context.Context, id string) (upstream.AccountUsage, error) {
	token, err := s.pool.AccessToken(ctx, id)
	if err != nil {
		return upstream.AccountUsage{}, err
	}
	usage, err := upstream.FetchUsage(ctx, s.httpClient, token)
	if err != nil {
		return upstream.AccountUsage{}, err
	}

	q := store.AccountQuota{UpdatedAt: usage.FetchedAt}
	if w := usage.FiveHour; w != nil {
		q.FiveHourUtil = w.Utilization
		q.FiveHourReset = parseUsageTime(w.ResetsAt)
		q.FiveHourStatus = lockedOrAllowed(w.LockedReason)
	} else {
		q.FiveHourUtil = -1
	}
	if w := usage.SevenDay; w != nil {
		q.SevenDayUtil = w.Utilization
		q.SevenDayReset = parseUsageTime(w.ResetsAt)
		q.SevenDayStatus = lockedOrAllowed(w.LockedReason)
	} else {
		q.SevenDayUtil = -1
	}
	// The limits array is passed through untouched: it is the server's own
	// normalised view, it is what the client renders, and re-deriving it here
	// would only be a second thing to keep in step with the upstream.
	if raw, err := json.Marshal(usage.Limits); err == nil {
		q.Detail = string(raw)
	}

	if err := s.store.SetAccountQuota(ctx, id, q); err != nil {
		return usage, err
	}
	return usage, nil
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

// runUsagePoller keeps every account's usage current.
//
// It polls rather than reading the figures off relayed responses, which is the
// whole point: an account that has not served anything still has a
// subscription, and an operator who has just connected one wants to see where
// it stands before sending it any traffic.
func (s *Server) runUsagePoller(ctx context.Context) {
	poll := func() {
		accounts, err := s.store.ListAccounts(ctx)
		if err != nil {
			s.log.Warn("could not list accounts for the usage poll", "err", err)
			return
		}
		for _, a := range accounts {
			if a.Disabled() {
				continue
			}
			if _, err := s.refreshAccountUsage(ctx, a.ID); err != nil {
				// Not an error worth raising: the account may simply be
				// unreachable this minute, and the UI shows the last figure
				// with the time it was taken.
				s.log.Debug("usage poll failed", "account", a.Email, "err", err)
			}
		}
	}

	poll()
	ticker := time.NewTicker(usagePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopSweeper:
			return
		case <-ticker.C:
			poll()
		}
	}
}
