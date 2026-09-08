package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// The schema a fresh database lands on, and the index migration 011 removes.
func TestSchemaAfterAllMigrations(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(context.Background(), filepath.Join(dir, "schema.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	var version int
	if err := st.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != 12 {
		t.Errorf("schema version = %d, want 12", version)
	}

	var n int
	if err := st.db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE name = 'idx_usage_key_at'`).Scan(&n); err != nil {
		t.Fatalf("look for idx_usage_key_at: %v", err)
	}
	if n != 0 {
		t.Error("idx_usage_key_at survived migration 011")
	}

	// The index every report actually uses has to still be there.
	if err := st.db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE name = 'idx_usage_at'`).Scan(&n); err != nil {
		t.Fatalf("look for idx_usage_at: %v", err)
	}
	if n != 1 {
		t.Error("idx_usage_at is missing; every usage query depends on it")
	}
}

// Usage and Totals have to agree: they are two queries over the same rows, and
// the overview screen reads one while the Usage tab reads the other.
func TestTotalsMatchesTheReport(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(context.Background(), filepath.Join(dir, "usage.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	now := time.Now().UTC()
	for i := range 40 {
		e := UsageEvent{
			At:    now.Add(-time.Duration(i) * time.Hour),
			KeyID: []string{"k1", "k2"}[i%2], KeyName: []string{"one", "two"}[i%2],
			AccountID: "a1", AccountEmail: "a@example.com",
			Model:       []string{"claude-opus-5", "claude-haiku-4-5"}[i%2],
			Status:      []int{200, 200, 429}[i%3],
			InputTokens: i, OutputTokens: i * 2,
			CacheReadTokens: i, CacheWriteTokens: i,
			Duration: time.Duration(i) * time.Millisecond,
		}
		if i%7 == 0 {
			e.Error = "overloaded_error: Overloaded"
		}
		if err := st.RecordUsage(ctx, e); err != nil {
			t.Fatalf("RecordUsage: %v", err)
		}
	}

	since := now.Add(-72 * time.Hour)
	rep, err := st.Usage(ctx, since)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	tot, err := st.Totals(ctx, since)
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}

	if rep.Totals.Requests != tot.Requests {
		t.Errorf("requests: report %d, totals %d", rep.Totals.Requests, tot.Requests)
	}
	if rep.Totals.Errors != tot.Errors {
		t.Errorf("errors: report %d, totals %d", rep.Totals.Errors, tot.Errors)
	}
	if rep.Totals.InputTokens != tot.InputTokens || rep.Totals.OutputTokens != tot.OutputTokens {
		t.Errorf("tokens: report %+v, totals %+v", rep.Totals, tot)
	}
	if rep.Totals.CacheReadTokens != tot.CacheReadTokens ||
		rep.Totals.CacheWriteTokens != tot.CacheWriteTokens {
		t.Errorf("cache: report %+v, totals %+v", rep.Totals, tot)
	}

	// And the breakdowns have to add back up to the totals they were folded
	// out of — the whole risk of computing five views in one pass.
	for name, buckets := range map[string][]UsageBucket{
		"by_day": rep.ByDay, "by_model": rep.ByModel,
		"by_key": rep.ByKey, "by_account": rep.ByAccount, "by_status": rep.ByStatus,
	} {
		var requests int64
		for _, b := range buckets {
			requests += b.Requests
		}
		if requests != rep.Totals.Requests {
			t.Errorf("%s sums to %d requests, totals say %d", name, requests, rep.Totals.Requests)
		}
	}

	if len(rep.ByModel) != 2 {
		t.Errorf("by_model has %d entries, want 2", len(rep.ByModel))
	}
	if len(rep.ByKey) != 2 {
		t.Errorf("by_key has %d entries, want 2", len(rep.ByKey))
	}
	// Busiest first.
	for i := 1; i < len(rep.ByStatus); i++ {
		if rep.ByStatus[i-1].Requests < rep.ByStatus[i].Requests {
			t.Errorf("by_status is not ordered by request count: %+v", rep.ByStatus)
			break
		}
	}
}
