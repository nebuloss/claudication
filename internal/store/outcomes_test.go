package store

import (
	"context"
	"testing"
	"time"
)

// A client's streak is the run of failures it is in now: a success ends it,
// and the latest failure is the one the notice names.
func TestClientOutcomes(t *testing.T) {
	st := usageStore(t)
	ctx := context.Background()
	now := time.Now().Add(-time.Minute)
	at := func(i int) time.Time { return now.Add(time.Duration(i) * time.Second) }

	record(t, st, UsageEvent{At: at(0), KeyID: "k1", Status: 500, Error: "boom"})
	record(t, st, UsageEvent{At: at(1), KeyID: "k1", Status: 200})
	for i := 2; i < 5; i++ {
		record(t, st, UsageEvent{At: at(i), KeyID: "k1", Status: 429, Error: "slow down", ErrorCode: "rate_limited"})
	}
	record(t, st, UsageEvent{At: at(5), KeyID: "k2", Status: 200})
	// Before the window: not counted.
	record(t, st, UsageEvent{At: now.Add(-time.Hour), KeyID: "k2", Status: 500, Error: "old"})

	got, err := st.ClientOutcomes(ctx, now.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	k1 := got["k1"]
	if k1 == nil || k1.Requests != 5 || k1.Failures != 4 || k1.Streak != 3 || k1.Status != 429 || k1.Code != "rate_limited" || k1.Error != "slow down" {
		t.Errorf("k1 = %+v, want 5 requests, 4 failed, the last 3 in a row, latest 429", k1)
	}
	if k2 := got["k2"]; k2 == nil || k2.Requests != 1 || k2.Failures != 0 || k2.Streak != 0 {
		t.Errorf("k2 = %+v, want one request that worked", k2)
	}

	n, last, err := st.CodeSeen(ctx, k1.Code, now.Add(-time.Second))
	if err != nil || n != 3 || !last.Equal(at(4).Truncate(time.Millisecond)) {
		t.Errorf("CodeSeen(%q) = %d, %v, %v; want 3, the last at %v", k1.Code, n, last, err, at(4))
	}
}
