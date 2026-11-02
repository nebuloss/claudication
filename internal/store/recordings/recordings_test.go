package recordings

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func open(t *testing.T, dir string) *DB {
	t.Helper()
	d, err := Open(context.Background(), filepath.Join(dir, FileName(Version)), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func h(s string) []byte { return bytes.Repeat([]byte(s), 16)[:16] }

func put(t *testing.T, d *DB, event int64, key string, at time.Time, chunks ...string) {
	t.Helper()
	ctx := context.Background()
	var hashes [][]byte
	for _, c := range chunks {
		hashes = append(hashes, h(c))
	}
	missing, err := d.Missing(ctx, hashes)
	if err != nil {
		t.Fatal(err)
	}
	send := map[string][]byte{}
	for _, c := range chunks {
		if missing[string(h(c))] {
			send[string(h(c))] = bytes.Repeat([]byte(c), 4096)
		}
	}
	if err := d.Put(ctx, Recording{Event: event, At: at, KeyID: key, Conversation: "chat",
		Meta: []byte(fmt.Sprintf("meta %d", event)), Bytes: 10, Hashes: hashes}, send); err != nil {
		t.Fatal(err)
	}
}

// A recording is stored with its chunks, each chunk once however many
// recordings use it, and read back.
func TestStoredOnce(t *testing.T) {
	ctx := context.Background()
	d := open(t, t.TempDir())
	now := time.Now()
	put(t, d, 1, "k", now, "a", "b")
	missing, _ := d.Missing(ctx, [][]byte{h("a"), h("b"), h("c")})
	if len(missing) != 1 || !missing[string(h("c"))] {
		t.Fatalf("missing = %v, want only c", missing)
	}
	put(t, d, 2, "k", now, "a", "b", "c")

	has, _ := d.Has(ctx, []int64{1, 2, 3})
	if !has[1] || !has[2] || has[3] {
		t.Errorf("has = %v", has)
	}
	r, err := d.Get(ctx, 2)
	if err != nil || string(r.Meta) != "meta 2" || r.KeyID != "k" || r.Conversation != "chat" {
		t.Fatalf("get = %+v, %v", r, err)
	}
	if _, err := d.Get(ctx, 3); err != ErrNotFound {
		t.Errorf("get of none = %v", err)
	}
	cs, _ := d.Chunks(ctx, [][]byte{h("a"), h("c")})
	if len(cs) != 2 || !bytes.HasPrefix(cs[string(h("c"))], []byte("cccc")) {
		t.Errorf("chunks = %d", len(cs))
	}
	var n int
	_ = d.read.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&n)
	if n != 3 {
		t.Errorf("%d chunks stored, want 3", n)
	}
}

// Clearing a client drops its recordings and the chunks only they used; a
// chunk another client's recording uses stays.
func TestClearSweeps(t *testing.T) {
	ctx := context.Background()
	d := open(t, t.TempDir())
	now := time.Now()
	put(t, d, 1, "k1", now, "shared", "only1")
	put(t, d, 2, "k2", now, "shared", "only2")
	if err := d.Clear(ctx, "k1"); err != nil {
		t.Fatal(err)
	}
	has, _ := d.Has(ctx, []int64{1, 2})
	if has[1] || !has[2] {
		t.Errorf("has = %v", has)
	}
	cs, _ := d.Chunks(ctx, [][]byte{h("shared"), h("only1"), h("only2")})
	if len(cs) != 2 || cs[string(h("only1"))] != nil {
		t.Errorf("chunks left = %d, want shared and only2", len(cs))
	}
	if err := d.Clear(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if u, _ := d.Usage(ctx); u.Recordings != 0 {
		t.Errorf("%d left after clearing all", u.Recordings)
	}
}

// Pruning drops what is older than the retention, then the oldest until the
// file is within its budget.
func TestPrune(t *testing.T) {
	ctx := context.Background()
	d := open(t, t.TempDir())
	old := time.Now().Add(-10 * 24 * time.Hour)
	put(t, d, 1, "k", old, "x")
	for i := int64(2); i <= 200; i++ {
		put(t, d, i, "k", time.Now().Add(time.Duration(i)*time.Second), fmt.Sprintf("c%d", i))
	}
	gone, err := d.Prune(ctx, 7*24*time.Hour, 0)
	if err != nil || gone != 1 {
		t.Fatalf("by age: %d gone, %v", gone, err)
	}
	u, _ := d.Usage(ctx)
	budget := u.Bytes / 2
	if _, err := d.Prune(ctx, 7*24*time.Hour, budget); err != nil {
		t.Fatal(err)
	}
	after, _ := d.Usage(ctx)
	if after.Bytes > budget || after.Recordings == 0 || after.Recordings >= u.Recordings {
		t.Errorf("by budget: %d bytes of %d budget, %d of %d recordings left", after.Bytes, budget, after.Recordings, u.Recordings)
	}
	has, _ := d.Has(ctx, []int64{2, 200})
	if has[2] || !has[200] {
		t.Errorf("pruned the wrong end: %v", has)
	}
}

// Two processes write the one file at once, as the two workers of a
// handover do.
func TestTwoWriters(t *testing.T) {
	dir := t.TempDir()
	a, b := open(t, dir), open(t, dir)
	var wg sync.WaitGroup
	for i, d := range []*DB{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 30 {
				put(t, d, int64(i*1000+j), "k", time.Now(), "shared", fmt.Sprintf("w%d-%d", i, j))
			}
		}()
	}
	wg.Wait()
	if u, _ := a.Usage(context.Background()); u.Recordings != 60 {
		t.Errorf("%d recordings, want 60", u.Recordings)
	}
}

// Another version's file goes; this one's stays.
func TestRemoveOthers(t *testing.T) {
	dir := t.TempDir()
	open(t, dir)
	for _, f := range []string{"capture-v0.db", "capture-v0.db-wal", "claudication.db"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	RemoveOthers(dir)
	for f, want := range map[string]bool{"capture-v0.db": false, "capture-v0.db-wal": false,
		FileName(Version): true, "claudication.db": true} {
		if _, err := os.Stat(filepath.Join(dir, f)); (err == nil) != want {
			t.Errorf("%s present = %v, want %v", f, err == nil, want)
		}
	}
}
