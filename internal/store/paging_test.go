package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func pagingFixture(t *testing.T, n int) (*Store, time.Time) {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(context.Background(), filepath.Join(dir, "paging.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	base := time.Now().UTC().Truncate(time.Second)
	for i := range n {
		// Oldest first, so row ids ascend with time the way real traffic does.
		if err := st.RecordUsage(context.Background(), UsageEvent{
			At: base.Add(time.Duration(i) * time.Second), KeyID: "k1", KeyName: "one",
			Model: "claude-haiku-4-5", Path: "/v1/messages", Status: 200,
			InputTokens: i,
		}); err != nil {
			t.Fatalf("RecordUsage: %v", err)
		}
	}
	return st, base
}

// Paging must show every row exactly once. The failure this guards against is
// the one OFFSET has: the list grows at the top, so page two re-shows rows page
// one already showed and hides the ones that fell past the boundary.
func TestPagingCoversEveryRowExactlyOnce(t *testing.T) {
	const total, page = 37, 10
	st, _ := pagingFixture(t, total)
	ctx := context.Background()

	seen := map[int]int{}
	var cursor UsageCursor
	var pages int
	for {
		rows, next, err := st.RecentUsage(ctx, page, cursor)
		if err != nil {
			t.Fatalf("RecentUsage: %v", err)
		}
		pages++
		for _, r := range rows {
			seen[r.InputTokens]++
		}
		if next.ID == 0 {
			break
		}
		cursor = next
		if pages > 20 {
			t.Fatal("paging did not terminate")
		}
	}

	if len(seen) != total {
		t.Errorf("saw %d distinct rows over %d pages, want %d", len(seen), pages, total)
	}
	for token, times := range seen {
		if times != 1 {
			t.Errorf("row %d appeared %d times", token, times)
		}
	}
}

// Newest first, and still newest first across a page boundary.
func TestPagingIsNewestFirst(t *testing.T) {
	st, _ := pagingFixture(t, 25)
	ctx := context.Background()

	first, next, err := st.RecentUsage(ctx, 10, UsageCursor{})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := st.RecentUsage(ctx, 10, next)
	if err != nil {
		t.Fatal(err)
	}

	all := append(append([]UsageEvent{}, first...), second...)
	for i := 1; i < len(all); i++ {
		if all[i].At.After(all[i-1].At) {
			t.Fatalf("row %d (%s) is newer than the one before it (%s)",
				i, all[i].At, all[i-1].At)
		}
	}
	if first[0].InputTokens != 24 {
		t.Errorf("first row = %d, want the newest (24)", first[0].InputTokens)
	}
}

// Rows arriving while someone is paging must not shift the boundary. This is
// the whole reason for a cursor rather than an offset.
func TestNewRowsDoNotDisturbAPageBoundary(t *testing.T) {
	st, base := pagingFixture(t, 20)
	ctx := context.Background()

	first, next, err := st.RecentUsage(ctx, 10, UsageCursor{})
	if err != nil {
		t.Fatal(err)
	}

	// Ten more requests are served before the operator asks for page two.
	for i := range 10 {
		if err := st.RecordUsage(ctx, UsageEvent{
			At: base.Add(time.Duration(100+i) * time.Second), KeyID: "k1",
			Path: "/v1/messages", Status: 200, InputTokens: 1000 + i,
		}); err != nil {
			t.Fatal(err)
		}
	}

	second, _, err := st.RecentUsage(ctx, 10, next)
	if err != nil {
		t.Fatal(err)
	}

	seen := map[int]bool{}
	for _, r := range first {
		seen[r.InputTokens] = true
	}
	for _, r := range second {
		if seen[r.InputTokens] {
			t.Errorf("row %d was shown twice; new arrivals shifted the boundary", r.InputTokens)
		}
		if r.InputTokens >= 1000 {
			t.Errorf("row %d arrived after paging started and appeared below the cursor", r.InputTokens)
		}
	}
}

// Two requests sharing a timestamp must not make a boundary ambiguous — which
// is why the cursor carries the row id and not just the time.
func TestPagingWithIdenticalTimestamps(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(context.Background(), filepath.Join(dir, "ties.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	at := time.Now().UTC().Truncate(time.Second)
	for i := range 10 {
		if err := st.RecordUsage(ctx, UsageEvent{
			At: at, KeyID: "k1", Path: "/v1/messages", Status: 200, InputTokens: i,
		}); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[int]int{}
	var cursor UsageCursor
	for range 10 {
		rows, next, err := st.RecentUsage(ctx, 3, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			seen[r.InputTokens]++
		}
		if next.ID == 0 {
			break
		}
		cursor = next
	}
	if len(seen) != 10 {
		t.Errorf("saw %d of 10 rows that share a timestamp", len(seen))
	}
	for token, times := range seen {
		if times != 1 {
			t.Errorf("row %d appeared %d times", token, times)
		}
	}
}

// The last page says it is the last, so the UI can stop offering more rather
// than finding out by fetching nothing.
func TestTheLastPageHasNoCursor(t *testing.T) {
	st, _ := pagingFixture(t, 5)
	rows, next, err := st.RecentUsage(context.Background(), 10, UsageCursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("rows = %d, want 5", len(rows))
	}
	if next.ID != 0 {
		t.Errorf("next cursor = %v, want empty on a short page", next)
	}
}

func TestUsageCursorRoundTrips(t *testing.T) {
	c := UsageCursor{At: time.Now().UTC().Truncate(time.Nanosecond), ID: 4242}
	got, ok := ParseUsageCursor(c.String())
	if !ok {
		t.Fatalf("could not parse %q", c.String())
	}
	if got.ID != c.ID || !got.At.Equal(c.At) {
		t.Errorf("round trip = %+v, want %+v", got, c)
	}

	// Anything unparseable means "start from the top", never an error.
	for _, bad := range []string{"", "nonsense", "not-a-time|5", "2026-01-01T00:00:00Z|abc", "|0"} {
		if _, ok := ParseUsageCursor(bad); ok {
			t.Errorf("accepted %q as a cursor", bad)
		}
	}
	if (UsageCursor{}).String() != "" {
		t.Error("an empty cursor should render as an empty string")
	}
}
