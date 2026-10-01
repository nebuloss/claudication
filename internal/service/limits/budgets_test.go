package limits

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeSpend answers KeySpend from a map and counts the reads.
type fakeSpend struct {
	mu     sync.Mutex
	spent  map[string]int64
	reads  int
	err    error
	lastAt time.Time
}

func (f *fakeSpend) KeySpend(_ context.Context, keyID string, since time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	f.lastAt = since
	if f.err != nil {
		return 0, f.err
	}
	return f.spent[keyID], nil
}

// The cache must not let a burst through on a stale reading, and must not
// re-query on every request either.
func TestTheBudgetCacheCreditsSpendBetweenRefreshes(t *testing.T) {
	src := &fakeSpend{spent: map[string]int64{"k": 100}}
	b := NewBudgets(src)
	ctx := context.Background()

	if got, err := b.SpentBy(ctx, "k"); err != nil || got != 100 {
		t.Fatalf("first read = %d, %v; want 100", got, err)
	}
	// More spending arrives, and is credited without a re-read.
	b.Add("k", 400)
	if got, _ := b.SpentBy(ctx, "k"); got != 500 {
		t.Errorf("cached figure = %d, want 500; spend must be credited between refreshes", got)
	}
	if src.reads != 1 {
		t.Errorf("reads = %d, want 1: a fresh figure must not re-query", src.reads)
	}
	// Once stale, it reads through again — the source knows only the 100.
	b.now = func() time.Time { return time.Now().Add(budgetFresh + time.Second) }
	if got, _ := b.SpentBy(ctx, "k"); got != 100 {
		t.Errorf("after expiry = %d, want 100 from the source", got)
	}
}

// The window asked of the source is the rolling day ending now.
func TestTheBudgetReadsTheRollingWindow(t *testing.T) {
	src := &fakeSpend{spent: map[string]int64{}}
	b := NewBudgets(src)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return at }
	if _, err := b.SpentBy(context.Background(), "k"); err != nil {
		t.Fatal(err)
	}
	if want := at.Add(-BudgetWindow); !src.lastAt.Equal(want) {
		t.Errorf("read since %s, want %s", src.lastAt, want)
	}
	if !b.Now().Equal(at) {
		t.Errorf("Now() = %s, want the tracker's clock", b.Now())
	}
}

// Raising a budget should unblock a client at once, not a refresh interval
// later.
func TestForgettingAKeyDropsItsCachedSpend(t *testing.T) {
	src := &fakeSpend{spent: map[string]int64{"k": 100}}
	b := NewBudgets(src)
	ctx := context.Background()
	if _, err := b.SpentBy(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	b.Add("k", 5000)
	if got, _ := b.SpentBy(ctx, "k"); got != 5100 {
		t.Fatalf("cached = %d, want 5100", got)
	}
	b.Forget("k")
	if got, _ := b.SpentBy(ctx, "k"); got != 100 {
		t.Errorf("after forget = %d, want a fresh read of 100", got)
	}
}

// A failed read is reported, not cached: the next request tries again.
func TestABudgetReadErrorIsNotCached(t *testing.T) {
	src := &fakeSpend{err: errors.New("database is closed")}
	b := NewBudgets(src)
	if _, err := b.SpentBy(context.Background(), "k"); err == nil {
		t.Fatal("error swallowed")
	}
	src.err = nil
	src.spent = map[string]int64{"k": 7}
	if got, err := b.SpentBy(context.Background(), "k"); err != nil || got != 7 {
		t.Errorf("after recovery = %d, %v; want 7", got, err)
	}
}

// Crediting nothing, or a key never read, changes nothing.
func TestAddIgnoresNothingAndUnknownKeys(t *testing.T) {
	b := NewBudgets(&fakeSpend{spent: map[string]int64{"k": 10}})
	b.Add("never-read", 100)
	if len(b.spent) != 0 {
		t.Error("crediting an unread key created an entry: it would read as fresh and skip the source")
	}
	if _, err := b.SpentBy(context.Background(), "k"); err != nil {
		t.Fatal(err)
	}
	b.Add("k", 0)
	b.Add("k", -5)
	if got, _ := b.SpentBy(context.Background(), "k"); got != 10 {
		t.Errorf("non-positive credits changed the figure to %d", got)
	}
}

func TestBudgetSweepDropsIdleKeys(t *testing.T) {
	b := NewBudgets(&fakeSpend{})
	b.spent["old"] = &budgetEntry{tokens: 1, refreshed: time.Now().Add(-2 * time.Hour)}
	b.spent["new"] = &budgetEntry{tokens: 1, refreshed: time.Now()}
	b.Sweep(time.Hour)
	if _, ok := b.spent["old"]; ok {
		t.Error("an idle entry was kept")
	}
	if _, ok := b.spent["new"]; !ok {
		t.Error("a live entry was dropped")
	}
}

// The sweepers run until told to stop, and stop when told.
func TestSweepersStopWhenAsked(t *testing.T) {
	b := NewBudgets(&fakeSpend{})
	b.spent["old"] = &budgetEntry{refreshed: time.Now().Add(-time.Hour)}
	l := NewLimiter()
	l.AllowPerMinute("ip", 1)
	l.now = func() time.Time { return time.Now().Add(time.Hour) }

	stop := make(chan struct{})
	done := make(chan struct{}, 2)
	go func() { b.RunSweeper(stop, time.Millisecond, time.Minute); done <- struct{}{} }()
	go func() { l.RunSweeper(stop, time.Millisecond, time.Minute); done <- struct{}{} }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		b.mu.Lock()
		bn := len(b.spent)
		b.mu.Unlock()
		l.mu.Lock()
		ln := len(l.buckets)
		l.mu.Unlock()
		if bn == 0 && ln == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sweepers did not run: budgets=%d buckets=%d", bn, ln)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(stop)
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("a sweeper did not stop")
		}
	}
}
