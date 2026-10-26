package availability

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"claudication/internal/store"
)

type fakeStore struct {
	mu       sync.Mutex
	accounts map[string]store.Account
	saved    []store.ModelTest
	failing  map[string][]store.ModelTest
}

func (f *fakeStore) Account(_ context.Context, id string) (store.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.accounts[id]
	if !ok {
		return store.Account{}, errors.New("no such account")
	}
	return a, nil
}

func (f *fakeStore) SaveModelTest(_ context.Context, _ string, t store.ModelTest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = append(f.saved, t)
	return nil
}

func (f *fakeStore) FailingModelTests(context.Context) (map[string][]store.ModelTest, error) {
	return f.failing, nil
}

func (f *fakeStore) savedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.saved)
}

type fakeHolds struct {
	mu    sync.Mutex
	until map[string]time.Time
}

func (h *fakeHolds) key(id, model string) string { return id + "/" + store.ModelKey(model) }

func (h *fakeHolds) HoldModel(id, model string, until time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.until == nil {
		h.until = map[string]time.Time{}
	}
	if until.After(h.until[h.key(id, model)]) {
		h.until[h.key(id, model)] = until
	}
}

func (h *fakeHolds) ReleaseModel(id, model string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.until, h.key(id, model))
}

func (h *fakeHolds) ModelHeldUntil(id, model string) time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.until[h.key(id, model)]
}

func newChecker(probe ProbeFunc) (*Checker, *fakeStore, *fakeHolds) {
	st := &fakeStore{accounts: map[string]store.Account{"a": {ID: "a", Provider: "google"}}}
	h := &fakeHolds{}
	return &Checker{Store: st, Holds: h, Probe: probe,
		Models: func(context.Context, string) ([]string, error) {
			return []string{"gemini-2.5-pro", "gemini-4-argon-high", "gemini-3.8-flash-high"}, nil
		}}, st, h
}

// A scan tries every model the account lists, holds the account off the one
// that fails until it is checked again, and leaves the rest alone.
func TestScanHoldsTheFailingModel(t *testing.T) {
	c, st, h := newChecker(func(_ context.Context, _ store.Account, model string) store.ModelTest {
		if model == "gemini-2.5-pro" {
			return store.ModelTest{Status: 503, Error: "No capacity available"}
		}
		return store.ModelTest{OK: true, Status: 200}
	})
	c.Scan("a")
	for c.Scanning("a") {
		time.Sleep(5 * time.Millisecond)
	}
	if st.savedCount() != 3 {
		t.Fatalf("saved %d results, want 3", st.savedCount())
	}
	for _, s := range st.saved {
		if s.Source != store.TestByScan {
			t.Errorf("%s recorded as %q, want scan", s.Model, s.Source)
		}
	}
	if h.ModelHeldUntil("a", "gemini-2.5-pro").IsZero() {
		t.Error("the failing model was not held")
	}
	if !h.ModelHeldUntil("a", "gemini-4-argon-high").IsZero() {
		t.Error("a working model was held")
	}
}

// Traffic counts at no cost: a model already working is written down once a
// SeenEvery however many requests it serves; a refusal for it holds it, and
// the next success releases it at once.
func TestTrafficMovesAModel(t *testing.T) {
	c, st, h := newChecker(nil)
	base := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	now := base
	c.now = func() time.Time { return now }
	wait := func(n int) {
		t.Helper()
		for i := 0; i < 200 && st.savedCount() < n; i++ {
			time.Sleep(5 * time.Millisecond)
		}
		if st.savedCount() != n {
			t.Fatalf("saved %d, want %d", st.savedCount(), n)
		}
	}

	c.Served("a", "gemini-3.8-flash-high")
	c.Served("a", "gemini-3.8-flash-high")
	wait(1)
	now = base.Add(SeenEvery)
	c.Served("a", "gemini-3.8-flash-high")
	wait(2)

	c.Refused("a", "gemini-2.5-pro", 503, "No capacity available")
	c.Refused("a", "gemini-2.5-pro", 503, "No capacity available")
	wait(3)
	for i := 0; i < 200 && h.ModelHeldUntil("a", "gemini-2.5-pro").IsZero(); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if got := h.ModelHeldUntil("a", "gemini-2.5-pro"); !got.Equal(now.Add(Recheck)) {
		t.Errorf("held until %v, want the next check, %v", got, now.Add(Recheck))
	}
	c.Served("a", "gemini-2.5-pro")
	wait(4)
	for i := 0; i < 200 && !h.ModelHeldUntil("a", "gemini-2.5-pro").IsZero(); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if !h.ModelHeldUntil("a", "gemini-2.5-pro").IsZero() {
		t.Error("a model that served a request is still held")
	}
}

// A failing model is checked again once Recheck has passed, and not while
// the upstream's own reset is later; a restart picks the failing ones up.
func TestRecheckWaitsForItsTurn(t *testing.T) {
	probes := 0
	c, st, h := newChecker(func(context.Context, store.Account, string) store.ModelTest {
		probes++
		return store.ModelTest{OK: true, Status: 200}
	})
	base := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	now := base
	c.now = func() time.Time { return now }
	st.failing = map[string][]store.ModelTest{"a": {{Model: "gemini-2.5-pro", At: base}}}
	if err := c.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !h.ModelHeldUntil("a", "gemini-2.5-pro").Equal(base.Add(Recheck)) {
		t.Fatal("a model failing before the restart is not held")
	}

	now = base.Add(Recheck - time.Second)
	c.recheck(context.Background())
	if probes != 0 {
		t.Fatal("checked before its turn")
	}
	// The upstream said it is back later than the check would be.
	h.HoldModel("a", "gemini-2.5-pro", base.Add(time.Hour))
	now = base.Add(Recheck)
	c.recheck(context.Background())
	if probes != 0 {
		t.Fatal("checked before the upstream's reset")
	}
	now = base.Add(time.Hour)
	c.recheck(context.Background())
	if probes != 1 || !h.ModelHeldUntil("a", "gemini-2.5-pro").IsZero() {
		t.Errorf("probes %d, held until %v", probes, h.ModelHeldUntil("a", "gemini-2.5-pro"))
	}
	now = base.Add(2 * time.Hour)
	c.recheck(context.Background())
	if probes != 1 {
		t.Error("a model that works again was checked again")
	}
}
