package httpapi

import (
	"context"
	"encoding/json"
	"time"

	"claudication/internal/store"
	"claudication/internal/upstream"
)

// How often every account's subscription usage is refreshed.
//
// Two rates, because the two situations want opposite things. With nobody
// watching, the figures only need to be honest by the time someone looks, and
// the polite thing is to leave the upstream alone. With the accounts screen
// open, the figures ARE the screen: an operator who clicks Refresh and watches
// the percentage move is asking a question the slow rate cannot answer, and
// before this the only live figure in the UI was the one they fetched by hand.
//
// Both rates are usage.poll-idle and usage.poll-watched in config: the fast one
// is traffic on the operator's own subscriptions, and where the usage endpoint
// starts refusing has not been measured.
const (
	// How long after an admin request the UI counts as still open, at the
	// least. adminWatchWindow stretches it to a few fast intervals, so a
	// browser tab refreshing at that rate keeps itself in the watched state
	// without a gap however slow the operator configured it.
	minAdminWatchWindow = 2 * time.Minute

	// The poller wakes this often and decides whether it is due. Sleeping in
	// short steps is what lets the rate change take effect immediately when
	// someone opens the UI, rather than after the current long sleep ends.
	// No finer than config.MinUsagePoll allows either rate to be.
	usagePollTick = 5 * time.Second
)

func (s *Server) adminWatchWindow() time.Duration {
	return max(minAdminWatchWindow, 3*s.cfg.Usage.PollWatched.D())
}

// noteAdminActivity records that the admin UI asked for something. Called from
// the admin middleware, so it covers every panel without each one opting in.
func (s *Server) noteAdminActivity() {
	s.adminSeen.Store(time.Now().UnixNano())
}

// adminWatching reports whether the admin UI has been heard from recently.
func (s *Server) adminWatching() bool {
	last := s.adminSeen.Load()
	return last != 0 && time.Since(time.Unix(0, last)) < s.adminWatchWindow()
}

// usagePollInterval is the rate the poller should currently run at.
func (s *Server) usagePollInterval() time.Duration {
	if s.adminWatching() {
		return s.cfg.Usage.PollWatched.D()
	}
	return s.cfg.Usage.PollIdle.D()
}

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
	last := time.Now()

	ticker := time.NewTicker(usagePollTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopSweeper:
			return
		case <-ticker.C:
			if time.Since(last) < s.usagePollInterval() {
				continue
			}
			poll()
			// Measured from the end of the round, so a slow upstream stretches
			// the gap instead of queueing polls back to back.
			last = time.Now()
		}
	}
}
