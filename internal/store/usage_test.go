package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func usageStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func record(t *testing.T, st *Store, e UsageEvent) {
	t.Helper()
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if e.Path == "" {
		e.Path = "/v1/messages"
	}
	if err := st.RecordUsage(context.Background(), e); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
}

func TestUsageAggregates(t *testing.T) {
	st := usageStore(t)
	ctx := context.Background()
	now := time.Now()

	record(t, st, UsageEvent{At: now, KeyID: "k1", KeyName: "laptop",
		Model: "claude-haiku-4-5", Status: 200,
		InputTokens: 100, OutputTokens: 50, Duration: 200 * time.Millisecond})
	record(t, st, UsageEvent{At: now, KeyID: "k1", KeyName: "laptop",
		Model: "claude-haiku-4-5", Status: 200,
		InputTokens: 10, OutputTokens: 5, CacheReadTokens: 7,
		Duration: 400 * time.Millisecond})
	record(t, st, UsageEvent{At: now, KeyID: "k2", KeyName: "ci",
		Model: "claude-opus-5", Status: 429, Duration: 900 * time.Millisecond})
	// A stream that failed after its 200 is an error, not a success — this is
	// the case that a status-only count gets wrong.
	record(t, st, UsageEvent{At: now, KeyID: "k2", KeyName: "ci",
		Model: "claude-opus-5", Status: 200, Error: "overloaded_error",
		Duration: 100 * time.Millisecond})

	rep, err := st.Usage(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if rep.Totals.Requests != 4 {
		t.Errorf("requests = %d, want 4", rep.Totals.Requests)
	}
	if rep.Totals.Errors != 2 {
		t.Errorf("errors = %d, want 2 (a 429 and a failed stream)", rep.Totals.Errors)
	}
	if rep.Totals.InputTokens != 110 || rep.Totals.OutputTokens != 55 {
		t.Errorf("tokens = %d/%d, want 110/55",
			rep.Totals.InputTokens, rep.Totals.OutputTokens)
	}
	if rep.Totals.CacheReadTokens != 7 {
		t.Errorf("cache reads = %d, want 7", rep.Totals.CacheReadTokens)
	}
	if rep.Totals.P95MS < rep.Totals.MedianMS {
		t.Errorf("p95 (%d) below median (%d)", rep.Totals.P95MS, rep.Totals.MedianMS)
	}

	byModel := map[string]int64{}
	for _, b := range rep.ByModel {
		byModel[b.Label] = b.Requests
	}
	if byModel["claude-haiku-4-5"] != 2 || byModel["claude-opus-5"] != 2 {
		t.Errorf("by model = %v", byModel)
	}

	byKey := map[string]int64{}
	for _, b := range rep.ByKey {
		byKey[b.Label] = b.Requests
	}
	if byKey["laptop"] != 2 || byKey["ci"] != 2 {
		t.Errorf("by key = %v", byKey)
	}
}

// The window is the point: a report over the last hour must not include
// yesterday.
func TestUsageRespectsTheWindow(t *testing.T) {
	st := usageStore(t)
	now := time.Now()

	record(t, st, UsageEvent{At: now.Add(-48 * time.Hour), KeyID: "k", Status: 200})
	record(t, st, UsageEvent{At: now, KeyID: "k", Status: 200})

	rep, err := st.Usage(context.Background(), now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Totals.Requests != 1 {
		t.Errorf("requests in the last hour = %d, want 1", rep.Totals.Requests)
	}
}

// Attribution has to survive the key going away, or the record you go looking
// for after deleting a key is exactly the one that vanished.
func TestUsageOutlivesItsKey(t *testing.T) {
	st := usageStore(t)
	ctx := context.Background()

	key, _, err := st.CreateKey(ctx, "temporary", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	record(t, st, UsageEvent{KeyID: key.ID, KeyName: key.Name, Status: 200, InputTokens: 5})

	if err := st.DeleteKey(ctx, key.ID); err != nil {
		t.Fatal(err)
	}

	rep, err := st.Usage(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.ByKey) != 1 || rep.ByKey[0].Label != "temporary" {
		t.Fatalf("by key after deleting it = %+v, want the name preserved", rep.ByKey)
	}
}

func TestPruneUsageDropsOnlyOldRows(t *testing.T) {
	st := usageStore(t)
	ctx := context.Background()
	now := time.Now()

	record(t, st, UsageEvent{At: now.Add(-40 * 24 * time.Hour), Status: 200})
	record(t, st, UsageEvent{At: now.Add(-40 * 24 * time.Hour), Status: 200})
	record(t, st, UsageEvent{At: now, Status: 200})

	n, err := st.PruneUsage(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("PruneUsage: %v", err)
	}
	if n != 2 {
		t.Errorf("pruned %d rows, want 2", n)
	}

	events, err := st.RecentUsage(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Errorf("%d rows left, want 1", len(events))
	}

	// A retention of 0 means "keep everything", not "delete everything".
	if n, err := st.PruneUsage(ctx, 0); err != nil || n != 0 {
		t.Errorf("PruneUsage(0) = %d, %v; want 0, nil", n, err)
	}
}

func TestRecentUsageIsNewestFirst(t *testing.T) {
	st := usageStore(t)
	now := time.Now()

	record(t, st, UsageEvent{At: now.Add(-2 * time.Minute), Model: "older", Status: 200})
	record(t, st, UsageEvent{At: now, Model: "newer", Status: 200})

	events, err := st.RecentUsage(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Model != "newer" {
		t.Fatalf("recent = %+v, want newest first", events)
	}
	if events[0].Duration != 0 {
		t.Errorf("duration round-tripped as %v, want 0", events[0].Duration)
	}
}
