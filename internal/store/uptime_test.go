package store

import (
	"context"
	"testing"
	"time"
)

// A minute keeps the worst state sampled in it, reads come back oldest first
// from the moment asked, and pruning drops only what is older than its cutoff.
func TestUptimeSamples(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

	for _, s := range []struct {
		at    time.Duration
		state int
	}{
		{10 * time.Second, 1}, {20 * time.Second, 3}, {50 * time.Second, 1}, // one minute: 3 stands
		{time.Minute, 2},
		{-48 * time.Hour, 1},
	} {
		if err := st.RecordUptime(ctx, base.Add(s.at), s.state); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.UptimeSince(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].Minute.Equal(base) || got[0].State != 3 ||
		!got[1].Minute.Equal(base.Add(time.Minute)) || got[1].State != 2 {
		t.Errorf("samples = %+v", got)
	}

	if n, err := st.PruneUptime(ctx, base.Add(-time.Hour)); err != nil || n != 1 {
		t.Errorf("pruned %d, %v; want the old sample", n, err)
	}
	if all, _ := st.UptimeSince(ctx, time.Time{}); len(all) != 2 {
		t.Errorf("after pruning, %d samples remain, want 2", len(all))
	}
}
