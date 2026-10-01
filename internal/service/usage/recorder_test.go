package usage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"claudication/internal/store"
)

type fakeStore struct {
	mu          sync.Mutex
	recorded    []store.UsageEvent
	recordErr   error
	pruned      []time.Duration
	pruneErr    error
	titlePrunes int
	titleErr    error
	pruneRows   int64
}

func (f *fakeStore) RecordUsage(_ context.Context, e store.UsageEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordErr != nil {
		return f.recordErr
	}
	f.recorded = append(f.recorded, e)
	return nil
}

func (f *fakeStore) PruneUsage(_ context.Context, keep time.Duration) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruned = append(f.pruned, keep)
	return f.pruneRows, f.pruneErr
}

func (f *fakeStore) PruneChatTitles(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.titlePrunes++
	return 1, f.titleErr
}

type credits map[string]int64

func (c credits) Add(id string, n int64) { c[id] += n }

func TestRecordClassifiesAndCreditsTheBudget(t *testing.T) {
	st, cr := &fakeStore{}, credits{}
	r := &Recorder{Store: st, Retention: 24 * time.Hour, Budgets: cr,
		Classify: func(status int, text string) string {
			if status >= 400 {
				return "rate_limit_error"
			}
			return ""
		}}
	e := store.UsageEvent{KeyID: "k", Status: 429, Error: "slow down", InputTokens: 3, OutputTokens: 4}
	r.Record(e, 1000)
	if len(st.recorded) != 1 || st.recorded[0].ErrorCode != "rate_limit_error" {
		t.Fatalf("recorded %+v", st.recorded)
	}
	if cr["k"] != e.Tokens() {
		t.Errorf("budget credited %d, want %d", cr["k"], e.Tokens())
	}
}

// An unlimited key does not touch the tracker, nor does a keyless request.
func TestRecordCreditsOnlyKeysWithABudget(t *testing.T) {
	cr := credits{}
	r := &Recorder{Store: &fakeStore{}, Retention: time.Hour, Budgets: cr}
	r.Record(store.UsageEvent{KeyID: "unlimited", OutputTokens: 5}, 0)
	r.Record(store.UsageEvent{KeyID: "", OutputTokens: 5}, 100)
	if len(cr) != 0 {
		t.Errorf("credited %v", cr)
	}
	(&Recorder{Store: &fakeStore{}, Retention: time.Hour}).Record(store.UsageEvent{KeyID: "k"}, 10) // nil Budgets
}

// Retention zero records nothing; a store failure loses the row, not the
// caller, and credits nothing it could not record.
func TestRecordIsBestEffort(t *testing.T) {
	st := &fakeStore{}
	(&Recorder{Store: st}).Record(store.UsageEvent{KeyID: "k"}, 10)
	if len(st.recorded) != 0 {
		t.Error("recorded with retention off")
	}

	st.recordErr = errors.New("disk full")
	cr := credits{}
	(&Recorder{Store: st, Retention: time.Hour, Budgets: cr}).Record(store.UsageEvent{KeyID: "k", OutputTokens: 9}, 10)
	if len(cr) != 0 {
		t.Error("credited a request that was not recorded")
	}
}

func TestPrunePrunesEventsThenTitles(t *testing.T) {
	st := &fakeStore{pruneRows: 3}
	r := &Recorder{Store: st, Retention: 30 * 24 * time.Hour}
	r.Prune(context.Background())
	if len(st.pruned) != 1 || st.pruned[0] != 30*24*time.Hour || st.titlePrunes != 1 {
		t.Errorf("pruned %v, titles %d", st.pruned, st.titlePrunes)
	}

	// A failed event prune does not go on to prune titles against events
	// that are still there.
	st2 := &fakeStore{pruneErr: errors.New("locked")}
	(&Recorder{Store: st2, Retention: time.Hour}).Prune(context.Background())
	if st2.titlePrunes != 0 {
		t.Error("titles pruned after the event prune failed")
	}
	// A failed title prune is reported, not fatal.
	(&Recorder{Store: &fakeStore{titleErr: errors.New("x")}, Retention: time.Hour}).Prune(context.Background())
}

func TestRunPrunerPrunesAtOnceAndStops(t *testing.T) {
	st := &fakeStore{}
	r := &Recorder{Store: st, Retention: time.Hour}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { r.RunPruner(stop, 10*time.Millisecond); close(done) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		st.mu.Lock()
		n := len(st.pruned)
		st.mu.Unlock()
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pruned %d times", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pruner did not stop")
	}

	// Retention off: returns at once, prunes nothing.
	off := &fakeStore{}
	(&Recorder{Store: off}).RunPruner(make(chan struct{}), time.Hour)
	if len(off.pruned) != 0 {
		t.Error("pruned with retention off")
	}
}
