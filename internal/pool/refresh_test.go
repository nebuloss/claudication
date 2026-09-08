package pool

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"claudication/internal/oauth"
	"claudication/internal/secret"
	"claudication/internal/store"
)

// refreshFixture is a pool over a real store with one connected account, and a
// token exchange the test drives.
func refreshFixture(t *testing.T) (*Pool, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	sealer, err := secret.Load(dir)
	if err != nil {
		t.Fatalf("secret.Load: %v", err)
	}

	acct, err := st.UpsertAccount(context.Background(), sealer,
		store.Account{
			Provider:  "anthropic",
			Email:     "only@example.com",
			ExpiresAt: time.Now().Add(-time.Minute), // due a refresh
		},
		store.Tokens{AccessToken: "old-access", RefreshToken: "old-refresh"})
	if err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}

	p := New(st, sealer, &http.Client{}, slog.New(slog.DiscardHandler))
	return p, st, acct.ID
}

// The bug this pins: Anthropic rotates the refresh token on every exchange, so
// between the upstream answering and the new token reaching the database there
// is a window in which the only usable credential is in memory. Running that
// window on the caller's context means a client pressing Esc — or a shutdown,
// or a request deadline — spends the old token upstream and loses the new one,
// and the account is dead until a human redoes the browser flow.
func TestRefreshSurvivesTheCallerGivingUp(t *testing.T) {
	p, st, id := refreshFixture(t)

	ctx, cancel := context.WithCancel(context.Background())
	reached := make(chan struct{})
	p.exchange = func(exchangeCtx context.Context, _ *http.Client, refreshToken string) (oauth.Result, error) {
		close(reached)
		// The caller gives up exactly here: the old token is now spent
		// upstream and only this function holds its replacement.
		cancel()
		if err := exchangeCtx.Err(); err != nil {
			t.Errorf("the exchange context followed the caller's cancellation: %v", err)
		}
		return oauth.Result{
			AccessToken:  "new-access",
			RefreshToken: "new-refresh",
			ExpiresAt:    time.Now().Add(time.Hour),
		}, nil
	}

	if err := p.Refresh(ctx, id); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	<-reached

	tokens, err := st.AccountTokens(context.Background(), p.sealer, id)
	if err != nil {
		t.Fatalf("AccountTokens: %v", err)
	}
	if tokens.RefreshToken != "new-refresh" {
		t.Fatalf("stored refresh token = %q, want new-refresh; the rotated token was lost and this account is now bricked",
			tokens.RefreshToken)
	}
	if tokens.AccessToken != "new-access" {
		t.Errorf("stored access token = %q, want new-access", tokens.AccessToken)
	}
}

// A waiter on somebody else's in-flight refresh has to be told what actually
// happened. Returning nil regardless hands it the old, expired token and then
// blames it for the 401 that follows — and, worse, that 401 cools the account
// down for a failure it did not cause.
func TestRefreshWaitersLearnItFailed(t *testing.T) {
	p, _, id := refreshFixture(t)

	release := make(chan struct{})
	started := make(chan struct{})
	wantErr := errors.New("upstream said no")

	p.exchange = func(context.Context, *http.Client, string) (oauth.Result, error) {
		close(started)
		<-release
		return oauth.Result{}, wantErr
	}

	var wg sync.WaitGroup
	first := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		first <- p.Refresh(context.Background(), id)
	}()

	<-started

	// Now a second caller arrives and finds a refresh already in flight.
	second := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		second <- p.Refresh(context.Background(), id)
	}()

	// Give the waiter a moment to reach the wait rather than the fast path.
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	if err := <-first; !errors.Is(err, wantErr) {
		t.Errorf("the refreshing caller got %v, want %v", err, wantErr)
	}
	if err := <-second; !errors.Is(err, wantErr) {
		t.Errorf("the waiting caller got %v, want %v — a waiter told the refresh succeeded will use a token that was never replaced",
			err, wantErr)
	}
}

// Two callers must produce one exchange: a rotating refresh token means the
// loser of a race holds one that is already spent.
func TestRefreshIsSingleFlight(t *testing.T) {
	p, _, id := refreshFixture(t)

	var calls int
	var mu sync.Mutex
	release := make(chan struct{})
	started := make(chan struct{})

	p.exchange = func(context.Context, *http.Client, string) (oauth.Result, error) {
		mu.Lock()
		calls++
		if calls == 1 {
			close(started)
		}
		mu.Unlock()
		<-release
		return oauth.Result{
			AccessToken:  "new-access",
			RefreshToken: "new-refresh",
			ExpiresAt:    time.Now().Add(time.Hour),
		}, nil
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = p.Refresh(context.Background(), id) }()
	<-started
	wg.Add(1)
	go func() { defer wg.Done(); _ = p.Refresh(context.Background(), id) }()
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("exchanged the refresh token %d times, want 1", calls)
	}
}
