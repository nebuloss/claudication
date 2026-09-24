package httpapi

import (
	"testing"
	"time"
)

// The poll rate is the whole mechanism by which subscription usage appears to
// refresh on its own, so it is worth holding to its two states rather than
// trusting the constants to stay in a sensible relationship.
func TestUsagePollRateFollowsWhoIsWatching(t *testing.T) {
	srv, _, _ := newTestServer(t)

	if got := srv.usagePollInterval(); got != usagePollIdle {
		t.Errorf("a gateway nobody has looked at polls every %s, want %s", got, usagePollIdle)
	}

	srv.noteAdminActivity()
	if got := srv.usagePollInterval(); got != usagePollWatched {
		t.Errorf("with the UI open it polls every %s, want %s", got, usagePollWatched)
	}

	// Two minutes after the last admin request the tab is presumed closed.
	srv.adminSeen.Store(time.Now().Add(-adminWatchWindow - time.Second).UnixNano())
	if got := srv.usagePollInterval(); got != usagePollIdle {
		t.Errorf("after the watch window it polls every %s, want %s", got, usagePollIdle)
	}
}

// A browser polling at the rate the server advertises must keep itself in the
// watched state. If the window were shorter than the interval the rate would
// oscillate, and the figures would go stale for minutes at a time while
// someone was watching them.
func TestAdminWatchWindowOutlastsThePollInterval(t *testing.T) {
	if adminWatchWindow <= usagePollWatched {
		t.Fatalf("watch window %s must exceed the fast poll interval %s",
			adminWatchWindow, usagePollWatched)
	}
	if usagePollTick > usagePollWatched {
		t.Fatalf("the poller wakes every %s, too coarse to honour a %s interval",
			usagePollTick, usagePollWatched)
	}
}
