package pool

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"claudication/internal/oauth"
	"claudication/internal/store"
)

// Two processes overlap in a handover, and the lock makes them take turns.
// The one that waited must not then undo the other's work: the token it set
// out to replace is already replaced, and exchanging again rotates it for
// nothing — or, if the other found it dead, asks a provider that already said
// no.

func TestARefreshTheLockHolderDidIsNotRepeated(t *testing.T) {
	p, st := fixture(t)
	id := connect(t, p, "a@example.com", time.Now().Add(30*time.Second))
	calls := 0
	p.refreshers["anthropic"] = newTokens("mine", &calls)
	p.LockRefreshes(func(ctx context.Context, id string) (func(), error) {
		// The other process, finishing its exchange while this one waited.
		err := st.UpdateAccountTokens(ctx, id, store.Tokens{AccessToken: "theirs", RefreshToken: "theirs"},
			time.Now().Add(time.Hour), time.Time{})
		return func() {}, err
	})
	tok, err := p.AccessToken(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || tok != "theirs" {
		t.Errorf("exchanged %d times, token %q; want none, and the holder's token", calls, tok)
	}
}

func TestARefreshTheLockHolderFoundDeadStops(t *testing.T) {
	p, st := fixture(t)
	id := connect(t, p, "a@example.com", time.Now().Add(30*time.Second))
	calls := 0
	p.refreshers["anthropic"] = newTokens("mine", &calls)
	p.LockRefreshes(func(ctx context.Context, id string) (func(), error) {
		return func() {}, st.MarkRefreshDead(ctx, id, "invalid_grant")
	})
	err := p.Refresh(context.Background(), id)
	if !errors.Is(err, ErrNeedsReauth) || calls != 0 {
		t.Errorf("err %v after %d exchanges; want ErrNeedsReauth and none", err, calls)
	}
}

// A refresh nobody else did still happens after the wait: the lock is about
// order, not about skipping.
func TestARefreshAfterTheLockStillExchanges(t *testing.T) {
	p, _ := fixture(t)
	id := connect(t, p, "a@example.com", time.Now().Add(30*time.Second))
	calls := 0
	p.refreshers["anthropic"] = newTokens("mine", &calls)
	p.LockRefreshes(func(context.Context, string) (func(), error) { return func() {}, nil })
	tok, err := p.AccessToken(context.Background(), id)
	if err != nil || calls != 1 || tok != "mine" {
		t.Errorf("token %q, %d exchanges, err %v; want one exchange", tok, calls, err)
	}
}

// Waiting for the lock is not time taken from the exchange: one cut short
// after the provider rotated the token loses the only copy of the new one.
func TestWaitingForTheLockLeavesTheExchangeItsBudget(t *testing.T) {
	p, _ := fixture(t)
	id := connect(t, p, "a@example.com", time.Now().Add(30*time.Second))
	const waited = 300 * time.Millisecond
	var left time.Duration
	p.refreshers["anthropic"] = func(ctx context.Context, _ *http.Client, _ string) (oauth.Result, error) {
		d, _ := ctx.Deadline()
		left = time.Until(d)
		return oauth.Result{AccessToken: "mine", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	p.LockRefreshes(func(ctx context.Context, _ string) (func(), error) {
		if d, ok := ctx.Deadline(); !ok || time.Until(d) < refreshTimeout {
			t.Errorf("the lock wait has %v, want a budget longer than the exchange's", time.Until(d))
		}
		time.Sleep(waited)
		return func() {}, nil
	})
	if err := p.Refresh(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if left < refreshTimeout-waited/2 {
		t.Errorf("the exchange had %v of its %v after a %v wait for the lock", left, refreshTimeout, waited)
	}
}
