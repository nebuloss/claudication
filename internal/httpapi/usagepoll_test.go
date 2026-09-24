package httpapi

import (
	"testing"
	"time"

	"claudication/internal/config"
)

// The poll rate is the whole mechanism by which subscription usage appears to
// refresh on its own, so it is worth holding to its two states rather than
// trusting the constants to stay in a sensible relationship.
func TestUsagePollRateFollowsWhoIsWatching(t *testing.T) {
	srv, _, _ := newTestServer(t)
	usagePollIdle := srv.cfg.Usage.PollIdle.D()
	usagePollWatched := srv.cfg.Usage.PollWatched.D()

	if got := srv.usagePollInterval(); got != usagePollIdle {
		t.Errorf("a gateway nobody has looked at polls every %s, want %s", got, usagePollIdle)
	}

	srv.noteAdminActivity()
	if got := srv.usagePollInterval(); got != usagePollWatched {
		t.Errorf("with the UI open it polls every %s, want %s", got, usagePollWatched)
	}

	// Once the watch window has passed the tab is presumed closed.
	srv.adminSeen.Store(time.Now().Add(-srv.adminWatchWindow() - time.Second).UnixNano())
	if got := srv.usagePollInterval(); got != usagePollIdle {
		t.Errorf("after the watch window it polls every %s, want %s", got, usagePollIdle)
	}
}

// A browser polling at the rate the server advertises must keep itself in the
// watched state. If the window were shorter than the interval the rate would
// oscillate, and the figures would go stale for minutes at a time while
// someone was watching them.
func TestAdminWatchWindowOutlastsThePollInterval(t *testing.T) {
	srv, _, _ := newTestServer(t)
	for _, watched := range []time.Duration{config.MinUsagePoll, 20 * time.Second, 5 * time.Minute} {
		srv.cfg.Usage.PollWatched = config.Duration(watched)
		if w := srv.adminWatchWindow(); w <= watched {
			t.Errorf("watch window %s must exceed the fast poll interval %s", w, watched)
		}
	}
	if usagePollTick > config.MinUsagePoll {
		t.Fatalf("the poller wakes every %s, too coarse to honour a %s interval",
			usagePollTick, config.MinUsagePoll)
	}
}
