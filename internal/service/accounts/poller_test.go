package accounts

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
	accounts []store.Account
	listErr  error
	quotas   map[string]store.AccountQuota
	setErr   error
}

func (f *fakeStore) ListAccounts(context.Context) ([]store.Account, error) {
	return f.accounts, f.listErr
}

func (f *fakeStore) SetAccountQuota(_ context.Context, id string, q store.AccountQuota) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	if f.quotas == nil {
		f.quotas = map[string]store.AccountQuota{}
	}
	f.quotas[id] = q
	return nil
}

func (f *fakeStore) quotaCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.quotas)
}

type tokens map[string]string

func (t tokens) AccessToken(_ context.Context, id string) (string, error) {
	if tok, ok := t[id]; ok {
		return tok, nil
	}
	return "", errors.New("needs re-authorising")
}

func fetchUtil(u float64) FetchFunc {
	return func(_ context.Context, token string) (store.AccountQuota, error) {
		if token == "bad" {
			return store.AccountQuota{}, errors.New("401")
		}
		return store.AccountQuota{FiveHourUtil: u}, nil
	}
}

// The poll rate is the whole mechanism by which subscription usage appears to
// refresh on its own, so it is held to its two states.
func TestThePollRateFollowsWhoIsWatching(t *testing.T) {
	now := time.Now()
	p := &Poller{Idle: 5 * time.Minute, Watched: 20 * time.Second, now: func() time.Time { return now }}

	if got := p.Interval(); got != p.Idle {
		t.Errorf("nobody looking: every %s, want %s", got, p.Idle)
	}
	p.NoteActivity()
	if got := p.Interval(); got != p.Watched {
		t.Errorf("UI open: every %s, want %s", got, p.Watched)
	}
	now = now.Add(p.WatchWindow() + time.Second)
	if got := p.Interval(); got != p.Idle {
		t.Errorf("after the watch window: every %s, want %s", got, p.Idle)
	}
}

// A browser polling at the advertised rate must keep itself watched: a window
// shorter than the interval would oscillate and leave the figures stale.
func TestTheWatchWindowOutlastsThePollInterval(t *testing.T) {
	for _, watched := range []time.Duration{10 * time.Second, 20 * time.Second, 5 * time.Minute} {
		p := &Poller{Watched: watched}
		if w := p.WatchWindow(); w <= watched {
			t.Errorf("window %s must exceed the fast interval %s", w, watched)
		}
	}
	if Tick > 10*time.Second {
		t.Errorf("tick %s is too coarse for the 10 s minimum interval", Tick)
	}
}

func TestRefreshStoresWhatTheProviderReports(t *testing.T) {
	st := &fakeStore{}
	p := &Poller{Store: st, Tokens: tokens{"a": "tok"}, Fetch: fetchUtil(42)}
	if err := p.Refresh(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if st.quotas["a"].FiveHourUtil != 42 {
		t.Errorf("stored %+v", st.quotas["a"])
	}
	// Every failure on the way is the caller's to see.
	if err := p.Refresh(context.Background(), "no-token"); err == nil {
		t.Error("a missing token was not reported")
	}
	p.Tokens = tokens{"a": "bad"}
	if err := p.Refresh(context.Background(), "a"); err == nil {
		t.Error("a failed fetch was not reported")
	}
	st.setErr = errors.New("read-only")
	p.Tokens = tokens{"a": "tok"}
	if err := p.Refresh(context.Background(), "a"); err == nil {
		t.Error("a failed write was not reported")
	}
}

// Paused accounts are left alone; one failing does not stop the rest.
func TestPollAllSkipsPausedAccountsAndCarriesOn(t *testing.T) {
	pausedAt := time.Now()
	st := &fakeStore{accounts: []store.Account{
		{ID: "paused", DisabledAt: &pausedAt},
		{ID: "broken"},
		{ID: "fine"},
	}}
	p := &Poller{Store: st, Tokens: tokens{"paused": "tok", "fine": "tok"}, Fetch: fetchUtil(7)}
	p.PollAll(context.Background())
	if _, ok := st.quotas["paused"]; ok {
		t.Error("a paused account was polled")
	}
	if st.quotas["fine"].FiveHourUtil != 7 {
		t.Error("a healthy account was not polled after a broken one")
	}

	// A store that cannot list accounts is reported and survived.
	(&Poller{Store: &fakeStore{listErr: errors.New("closed")}}).PollAll(context.Background())
}

func TestRunPollsAtOnceThenWhenDueAndStops(t *testing.T) {
	st := &fakeStore{accounts: []store.Account{{ID: "a"}}}
	var mu sync.Mutex
	fetches := 0
	p := &Poller{Store: st, Tokens: tokens{"a": "tok"}, Idle: 20 * time.Millisecond, Watched: 10 * time.Millisecond,
		Fetch: func(context.Context, string) (store.AccountQuota, error) {
			mu.Lock()
			fetches++
			mu.Unlock()
			return store.AccountQuota{}, nil
		}}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { p.Run(context.Background(), stop, time.Millisecond); close(done) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := fetches
		mu.Unlock()
		if n >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("polled %d times", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("did not stop on stop")
	}

	// And on the context.
	ctx, cancel := context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() { p.Run(ctx, make(chan struct{}), time.Millisecond); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("did not stop on context")
	}
	if st.quotaCount() == 0 {
		t.Error("nothing stored")
	}
}
