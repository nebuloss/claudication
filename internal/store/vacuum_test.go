package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// A fresh database must be able to give space back. Without auto_vacuum,
// deleting rows only marks pages reusable and the file stays at its historical
// peak for ever — so lowering retention-days to recover disk recovers nothing.
func TestPruningReclaimsDiskSpace(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(context.Background(), filepath.Join(dir, "usage.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	if mode, err := st.AutoVacuum(ctx); err != nil {
		t.Fatalf("AutoVacuum: %v", err)
	} else if mode != 2 {
		t.Fatalf("auto_vacuum = %d, want 2 (incremental); a new database must be able to shrink", mode)
	}

	// Enough rows that the file is meaningfully larger than empty.
	old := time.Now().Add(-90 * 24 * time.Hour)
	for i := range 4000 {
		if err := st.RecordUsage(ctx, UsageEvent{
			At: old, KeyID: "k1", KeyName: "one",
			AccountID: "a1", AccountEmail: "a@example.com",
			Model: "claude-opus-5", Path: "/v1/messages", Status: 200,
			InputTokens: i, OutputTokens: i,
			// Something bulky, so the saving is unambiguous.
			Error: fmt.Sprintf("padding %0500d", i),
		}); err != nil {
			t.Fatalf("RecordUsage: %v", err)
		}
	}

	grown, err := st.fileSize(ctx)
	if err != nil {
		t.Fatal(err)
	}

	n, err := st.PruneUsage(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("PruneUsage: %v", err)
	}
	if n != 4000 {
		t.Fatalf("pruned %d rows, want 4000", n)
	}

	shrunk, err := st.fileSize(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("grew to %d bytes, pruned back to %d", grown, shrunk)

	if shrunk >= grown {
		t.Errorf("the file did not shrink after pruning: %d -> %d bytes", grown, shrunk)
	}
}

// Vacuum is the escape hatch for a database created before auto_vacuum was
// configured, and must be safe to run on one that needs nothing.
func TestVacuumIsSafeOnACompactDatabase(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(context.Background(), filepath.Join(dir, "compact.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	before, after, err := st.Vacuum(ctx)
	if err != nil {
		t.Fatalf("Vacuum: %v", err)
	}
	if after > before {
		t.Errorf("vacuum grew the file: %d -> %d", before, after)
	}

	// And the database still works afterwards.
	if err := st.RecordUsage(ctx, UsageEvent{
		At: time.Now(), Model: "claude-haiku-4-5", Path: "/v1/messages", Status: 200,
	}); err != nil {
		t.Errorf("the database is unusable after a vacuum: %v", err)
	}
}
