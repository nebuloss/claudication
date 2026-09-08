package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A snapshot has to be a whole database, taken while the gateway is serving.
//
// `cp` is the thing this replaces: in WAL mode committed rows live partly in
// the main file and partly in the -wal beside it, so copying the one file gets
// a database that is missing whatever was written most recently — which on a
// gateway is the account you just connected.
func TestSnapshotIsCompleteWhileWritesContinue(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(context.Background(), filepath.Join(dir, "live.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	if _, _, err := st.CreateKey(ctx, "before", 60, 250_000); err != nil {
		t.Fatal(err)
	}
	for i := range 200 {
		if err := st.RecordUsage(ctx, UsageEvent{
			At: time.Now(), KeyID: "k1", Path: "/v1/messages", Status: 200, InputTokens: i,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Writes keep arriving during the snapshot, as they would in production.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				_ = st.RecordUsage(ctx, UsageEvent{
					At: time.Now(), KeyID: "k2", Path: "/v1/messages", Status: 200,
				})
			}
		}
	}()

	snap := filepath.Join(dir, "snap.db")
	if err := st.Snapshot(ctx, snap); err != nil {
		close(stop)
		<-done
		t.Fatalf("Snapshot: %v", err)
	}
	close(stop)
	<-done

	// No -wal beside it: the point of VACUUM INTO is a self-contained file that
	// can be moved on its own.
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(snap + suffix); err == nil {
			t.Errorf("snapshot left a %s file beside it", suffix)
		}
	}

	// And it opens, migrates clean, and holds what was there.
	restored, err := Open(context.Background(), snap)
	if err != nil {
		t.Fatalf("the snapshot will not open: %v", err)
	}
	defer restored.Close()

	keys, err := restored.ListKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].Name != "before" {
		t.Fatalf("keys = %+v, want the one created before the snapshot", keys)
	}
	if keys[0].TokenBudget != 250_000 || keys[0].RPMLimit != 60 {
		t.Errorf("limits did not survive: budget %d, rpm %d",
			keys[0].TokenBudget, keys[0].RPMLimit)
	}

	var events int64
	if err := restored.db.QueryRow(`SELECT COUNT(*) FROM usage_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events < 200 {
		t.Errorf("snapshot holds %d events, want at least the 200 written before it", events)
	}
}

// Refusing to overwrite is SQLite's behaviour and worth keeping: a backup that
// silently replaces yesterday's is not a backup.
func TestSnapshotWillNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(context.Background(), filepath.Join(dir, "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	snap := filepath.Join(dir, "snap.db")
	if err := st.Snapshot(context.Background(), snap); err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	if err := st.Snapshot(context.Background(), snap); err == nil {
		t.Error("a second snapshot overwrote the first")
	}
}
