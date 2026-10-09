package pool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"claudication/internal/oauth"
	"claudication/internal/secret"
	"claudication/internal/store"
)

// fixture is a pool over a real, empty store whose token exchange fails the
// test unless the test replaces it: a refresh nobody asked for spends a
// rotating token, so an unexpected one is a bug, not a detail.
func fixture(t *testing.T) (*Pool, *store.Store) {
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
	p := New(st, sealer, &http.Client{}, slog.New(slog.DiscardHandler),
		func(context.Context, *http.Client, string) (oauth.Result, error) {
			t.Error("unexpected token exchange")
			return oauth.Result{}, errors.New("unexpected exchange")
		})
	return p, st
}

// connect adds an anthropic account whose access token is "access-<email>" and
// expires at expires.
func connect(t *testing.T, p *Pool, email string, expires time.Time) string {
	t.Helper()
	a, err := p.store.UpsertAccount(context.Background(), p.sealer,
		store.Account{Provider: "anthropic", Email: email, ExpiresAt: expires},
		store.Tokens{AccessToken: "access-" + email, RefreshToken: "refresh-" + email})
	if err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}
	return a.ID
}

// fresh is an expiry far enough out that no refresh is due.
func fresh() time.Time { return time.Now().Add(time.Hour) }

// newTokens is an exchange that always succeeds with the given access token.
func newTokens(access string, calls *int) RefreshFunc {
	return func(context.Context, *http.Client, string) (oauth.Result, error) {
		if calls != nil {
			*calls++
		}
		return oauth.Result{AccessToken: access, RefreshToken: "rotated", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
}

// The status decides both whether to retry on another account and how long
// this one sits out. A 4xx that is the caller's fault must not cool a healthy
// account down, and 529 (overloaded) must, or a single overload ends the
// retry loop without trying anyone else.
func TestClassifyStatus(t *testing.T) {
	for status, want := range map[int]FailureKind{
		429: FailureRateLimit,
		401: FailureAuth,
		403: FailureForbidden,
		500: FailureServer,
		502: FailureServer,
		529: FailureServer,
	} {
		got, ok := ClassifyStatus(status)
		if !ok || got != want {
			t.Errorf("ClassifyStatus(%d) = %q, %v; want %q, true", status, got, ok, want)
		}
	}
	for _, status := range []int{200, 400, 404, 413, 422} {
		if got, ok := ClassifyStatus(status); ok {
			t.Errorf("ClassifyStatus(%d) = %q, true; a client error must not count against the account", status, got)
		}
	}
}

// Consecutive failures back off exponentially up to a ceiling, so a broken
// account stops being tried on every request without being benched forever.
func TestReportFailureBacksOffAndCaps(t *testing.T) {
	p, st := fixture(t)
	id := connect(t, p, "a@example.com", fresh())
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return base }

	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	for i, w := range want {
		p.ReportFailure(id, FailureRateLimit, "slow down")
		p.mu.Lock()
		got := p.states[id].cooldownUntil.Sub(base)
		p.mu.Unlock()
		if got != w {
			t.Errorf("failure %d: cooldown %v, want %v", i+1, got, w)
		}
	}

	// The reason has to reach the operator, prefixed with what kind it was.
	a, err := st.Account(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if a.LastError != "rate_limit: slow down" {
		t.Errorf("LastError = %q", a.LastError)
	}
}

// A shift far past the exponent's ceiling must still land on the maximum and
// not wrap to zero or a negative wait, which would put the account straight
// back in rotation.
func TestReportFailureNeverOverflows(t *testing.T) {
	p, _ := fixture(t)
	id := connect(t, p, "a@example.com", fresh())
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return base }

	for i := 0; i < 80; i++ {
		p.ReportFailure(id, FailureAuth, "no")
	}
	p.mu.Lock()
	got := p.states[id].cooldownUntil.Sub(base)
	p.mu.Unlock()
	if got != time.Hour {
		t.Errorf("after 80 failures cooldown = %v, want the 1h cap", got)
	}
}

// A kind nobody listed still cools the account down, on the gentlest schedule,
// rather than panicking or letting it straight back in.
func TestReportFailureUnknownKindUsesServerBackoff(t *testing.T) {
	p, _ := fixture(t)
	id := connect(t, p, "a@example.com", fresh())
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return base }

	p.ReportFailure(id, FailureKind("mystery"), "?")
	p.mu.Lock()
	got := p.states[id].cooldownUntil.Sub(base)
	p.mu.Unlock()
	if got != 5*time.Second {
		t.Errorf("cooldown = %v, want the server base of 5s", got)
	}
}

// One success wipes the slate: an account that recovered must go back to the
// short first-failure wait, not resume the long one it had built up.
func TestReportSuccessClearsCooldown(t *testing.T) {
	p, st := fixture(t)
	id := connect(t, p, "a@example.com", fresh())
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return base }

	p.ReportFailure(id, FailureRateLimit, "x")
	p.ReportFailure(id, FailureRateLimit, "x")
	p.ReportSuccess(id)

	p.mu.Lock()
	h := p.states[id]
	cleared := h.cooldownUntil.IsZero() && h.failures == 0
	p.mu.Unlock()
	if !cleared {
		t.Fatalf("success left cooldown %v failures %d", h.cooldownUntil, h.failures)
	}
	a, err := st.Account(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if a.LastUsedAt == nil || a.LastError != "" {
		t.Errorf("store not updated: last used %v, last error %q", a.LastUsedAt, a.LastError)
	}

	p.ReportFailure(id, FailureRateLimit, "x")
	p.mu.Lock()
	got := p.states[id].cooldownUntil.Sub(base)
	p.mu.Unlock()
	if got != time.Minute {
		t.Errorf("first failure after a success cooled for %v, want the base minute", got)
	}
}

// Retry-After is what a client sleeps on. It must name the soonest recovery,
// and say nothing (0) when something can already serve.
func TestRetryAfter(t *testing.T) {
	p := newTestPool()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return base }
	all := []store.Account{account("a", 0, ""), account("b", 0, ""), account("c", 0, "")}

	if got := p.soonest(nil); got != 0 {
		t.Errorf("with no accounts, RetryAfter = %v, want 0", got)
	}
	p.coolDown("a", 3*time.Minute)
	p.coolDown("b", 40*time.Second)
	p.coolDown("c", time.Minute)
	if got := p.soonest(all); got != 40*time.Second {
		t.Errorf("RetryAfter = %v, want the soonest, 40s", got)
	}
	p.coolDown("c", 0)
	if got := p.soonest(all); got != 0 {
		t.Errorf("with one account free, RetryAfter = %v, want 0", got)
	}
}

// A paused account's expired cooldown is not a free account: it cannot serve,
// so it must not make Retry-After say "now".
func TestRetryAfterIgnoresAccountsThatCannotServe(t *testing.T) {
	p := newTestPool()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return base }

	paused := account("paused", 0, "")
	paused.Provider = "anthropic"
	paused.DisabledAt = &base
	live := account("live", 0, "")
	live.Provider = "anthropic"
	p.coolDown("paused", 0)
	p.coolDown("live", 40*time.Second)

	candidates := candidatesFor([]store.Account{paused, live}, "anthropic", "", nil, base)
	if got := p.soonest(candidates); got != 40*time.Second {
		t.Errorf("RetryAfter = %v, want 40s from the one account that can serve", got)
	}
}

// An account whose token is still good is served as is: refreshing early would
// spend a rotating refresh token for nothing.
func TestAcquireServesAValidTokenWithoutRefreshing(t *testing.T) {
	p, _ := fixture(t)
	id := connect(t, p, "a@example.com", fresh())

	lease, err := p.Acquire(context.Background(), "anthropic", "", nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if lease.Account.ID != id || lease.AccessToken != "access-a@example.com" {
		t.Errorf("lease = %s / %q", lease.Account.ID, lease.AccessToken)
	}
}

// A token about to expire is refreshed before it is handed out, and the lease
// carries the new one: a request must not ride a token that dies mid-flight.
func TestAcquireRefreshesANearlyExpiredToken(t *testing.T) {
	p, _ := fixture(t)
	connect(t, p, "a@example.com", time.Now().Add(30*time.Second))
	var calls int
	p.exchange = newTokens("brand-new", &calls)

	lease, err := p.Acquire(context.Background(), "anthropic", "", nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if calls != 1 {
		t.Errorf("exchanged %d times, want 1", calls)
	}
	if lease.AccessToken != "brand-new" {
		t.Errorf("lease token = %q, want the refreshed one", lease.AccessToken)
	}
	if !lease.Account.ExpiresAt.After(time.Now().Add(30 * time.Minute)) {
		t.Errorf("lease carries the stale expiry %v", lease.Account.ExpiresAt)
	}
}

// A failed refresh cools the account down so the next request does not walk
// straight into the same failure, and the error names the account.
func TestAcquireCoolsDownOnRefreshFailure(t *testing.T) {
	p, _ := fixture(t)
	id := connect(t, p, "a@example.com", time.Now().Add(-time.Minute))
	p.exchange = func(context.Context, *http.Client, string) (oauth.Result, error) {
		return oauth.Result{}, errors.New("provider down")
	}

	_, err := p.Acquire(context.Background(), "anthropic", "", nil)
	if err == nil || !strings.Contains(err.Error(), "a@example.com") {
		t.Fatalf("Acquire error = %v, want one naming the account", err)
	}
	st, err := p.Status(context.Background(), "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if _, cooling := st.Cooling[id]; !cooling {
		t.Error("an account whose refresh failed is not cooling down")
	}
}

// The two "nothing to serve" answers mean different things to the caller: no
// accounts is a setup problem, all excluded is a retry that ran out.
func TestAcquireEmptyAnswers(t *testing.T) {
	p, _ := fixture(t)
	if _, err := p.Acquire(context.Background(), "anthropic", "", nil); !errors.Is(err, ErrNoAccounts) {
		t.Errorf("empty pool: %v, want ErrNoAccounts", err)
	}
	id := connect(t, p, "a@example.com", fresh())
	if _, err := p.Acquire(context.Background(), "openai", "", nil); !errors.Is(err, ErrNoAccounts) {
		t.Errorf("other provider: %v, want ErrNoAccounts", err)
	}
	if _, err := p.Acquire(context.Background(), "anthropic", "", map[string]bool{id: true}); !errors.Is(err, ErrAllCoolingUp) {
		t.Errorf("all excluded: %v, want ErrAllCoolingUp", err)
	}
	// An empty exclude set is not a retry, so it is still "no accounts".
	if err := p.store.SetAccountDisabled(context.Background(), id, true); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Acquire(context.Background(), "anthropic", "", map[string]bool{}); !errors.Is(err, ErrNoAccounts) {
		t.Errorf("only a disabled account: %v, want ErrNoAccounts", err)
	}
}

// A retry skips the account that already failed this request and lands on the
// next one, in priority order.
func TestAcquireHonoursExclude(t *testing.T) {
	p, _ := fixture(t)
	first := connect(t, p, "first@example.com", fresh())
	second := connect(t, p, "second@example.com", fresh())

	lease, err := p.Acquire(context.Background(), "anthropic", "", nil)
	if err != nil || lease.Account.ID != first {
		t.Fatalf("Acquire = %s, %v; want the first account", lease.Account.ID, err)
	}
	lease, err = p.Acquire(context.Background(), "anthropic", "", map[string]bool{first: true})
	if err != nil || lease.Account.ID != second {
		t.Fatalf("retry Acquire = %s, %v; want the second account", lease.Account.ID, err)
	}
}

// A dead refresh token keeps serving while its access token lasts — free
// service while the operator notices — and drops out once that has gone,
// rather than producing a failed refresh per request.
func TestCandidatesWithADeadRefreshToken(t *testing.T) {
	now := time.Now()
	dead := now.Add(-time.Hour)
	alive := store.Account{ID: "alive", Provider: "anthropic", ExpiresAt: now.Add(time.Hour), RefreshDeadAt: &dead}
	gone := store.Account{ID: "gone", Provider: "anthropic", ExpiresAt: now.Add(-time.Second), RefreshDeadAt: &dead}
	disabled := store.Account{ID: "off", Provider: "anthropic", ExpiresAt: now.Add(time.Hour), DisabledAt: &dead}

	got := candidatesFor([]store.Account{alive, gone, disabled}, "anthropic", "", nil, now)
	if len(got) != 1 || got[0].ID != "alive" {
		ids := []string{}
		for _, a := range got {
			ids = append(ids, a.ID)
		}
		t.Errorf("candidates = %v, want only alive", ids)
	}
}

// Status is what the admin UI shows; it must agree with what Acquire would do,
// and count the accounts that waiting will not fix.
func TestStatusReportsThePoolsView(t *testing.T) {
	p, st := fixture(t)
	ctx := context.Background()
	first := connect(t, p, "first@example.com", fresh())
	second := connect(t, p, "second@example.com", fresh())
	third := connect(t, p, "third@example.com", fresh())
	dead := connect(t, p, "dead@example.com", fresh())
	if err := st.MarkRefreshDead(ctx, dead, "invalid_grant"); err != nil {
		t.Fatal(err)
	}
	// The third is out of room, which the pool knows from its quota.
	if err := st.SetAccountQuota(ctx, third, store.AccountQuota{FiveHourUtil: 100, SevenDayUtil: 10, FiveHourStatus: "rejected"}); err != nil {
		t.Fatal(err)
	}

	p.ReportFailure(first, FailureRateLimit, "429")

	s, err := p.Status(ctx, "anthropic")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if s.Serving != second {
		t.Errorf("Serving = %s, want the second while the first cools", s.Serving)
	}
	if _, ok := s.Cooling[first]; !ok || len(s.Cooling) != 1 {
		t.Errorf("Cooling = %v, want only the first", s.Cooling)
	}
	// second and the dead one (its access token still has an hour) can serve;
	// third is out of room and first is cooling.
	if s.Usable != 2 {
		t.Errorf("Usable = %d, want 2", s.Usable)
	}
	if s.NeedsReauth != 1 {
		t.Errorf("NeedsReauth = %d, want 1", s.NeedsReauth)
	}

	lease, err := p.Acquire(ctx, "anthropic", "", nil)
	if err != nil || lease.Account.ID != s.Serving {
		t.Errorf("Acquire chose %s (%v) but Status said %s", lease.Account.ID, err, s.Serving)
	}
}

// With nothing connected, Status says so rather than naming a phantom.
func TestStatusOfAnEmptyPool(t *testing.T) {
	p, _ := fixture(t)
	s, err := p.Status(context.Background(), "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if s.Serving != "" || s.Usable != 0 || len(s.Cooling) != 0 {
		t.Errorf("empty pool status = %+v", s)
	}
}

// A store that cannot be read is an error, not an empty pool: "no accounts"
// would send the operator to reconnect accounts that are fine.
func TestStoreErrorsSurface(t *testing.T) {
	p, st := fixture(t)
	connect(t, p, "a@example.com", fresh())
	st.Close()

	if _, err := p.Status(context.Background(), "anthropic"); err == nil {
		t.Error("Status on a closed store returned no error")
	}
	if _, err := p.Acquire(context.Background(), "anthropic", "", nil); err == nil || errors.Is(err, ErrNoAccounts) {
		t.Errorf("Acquire on a closed store = %v, want a store error", err)
	}
	if _, err := p.AccessToken(context.Background(), "whatever"); err == nil {
		t.Error("AccessToken on a closed store returned no error")
	}
}

// The usage poll asks for one specific account's token; it gets the stored one
// while valid and a refreshed one once it is due.
func TestAccessToken(t *testing.T) {
	p, _ := fixture(t)
	ctx := context.Background()
	valid := connect(t, p, "valid@example.com", fresh())
	due := connect(t, p, "due@example.com", time.Now().Add(-time.Minute))

	if tok, err := p.AccessToken(ctx, valid); err != nil || tok != "access-valid@example.com" {
		t.Errorf("valid account: %q, %v", tok, err)
	}

	var calls int
	p.exchange = newTokens("refreshed", &calls)
	if tok, err := p.AccessToken(ctx, due); err != nil || tok != "refreshed" {
		t.Errorf("due account: %q, %v; want the refreshed token", tok, err)
	}
	if calls != 1 {
		t.Errorf("exchanged %d times, want 1", calls)
	}

	if _, err := p.AccessToken(ctx, "missing"); !errors.Is(err, store.ErrAccountNotFound) {
		t.Errorf("missing account: %v, want ErrAccountNotFound", err)
	}

	p.exchange = func(context.Context, *http.Client, string) (oauth.Result, error) {
		return oauth.Result{}, errors.New("boom")
	}
	other := connect(t, p, "other@example.com", time.Now().Add(-time.Minute))
	if _, err := p.AccessToken(ctx, other); err == nil {
		t.Error("a failed refresh still produced a token")
	}
}

// invalid_grant is terminal. It must be recorded so that nothing asks again —
// retrying it is a loop that costs an error line and a cooldown each time —
// and the caller must be told the fix is a browser, not patience.
func TestRefreshInvalidGrantMarksTheAccountDead(t *testing.T) {
	p, st := fixture(t)
	ctx := context.Background()
	id := connect(t, p, "a@example.com", time.Now().Add(-time.Minute))

	calls := 0
	p.exchange = func(context.Context, *http.Client, string) (oauth.Result, error) {
		calls++
		return oauth.Result{}, fmt.Errorf("token endpoint: %w", oauth.ErrInvalidGrant)
	}

	if err := p.Refresh(ctx, id); !errors.Is(err, ErrNeedsReauth) {
		t.Fatalf("Refresh = %v, want ErrNeedsReauth", err)
	}
	a, err := st.Account(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !a.RefreshDead() {
		t.Fatal("invalid_grant was not recorded")
	}

	if err := p.Refresh(ctx, id); !errors.Is(err, ErrNeedsReauth) {
		t.Errorf("second Refresh = %v, want ErrNeedsReauth", err)
	}
	if calls != 1 {
		t.Errorf("exchanged %d times; a dead refresh token must not be offered again", calls)
	}
}

// Any other refusal is recorded against the account so the operator sees why,
// and is not mistaken for a dead token.
func TestRefreshTransientErrorIsRecorded(t *testing.T) {
	p, st := fixture(t)
	ctx := context.Background()
	id := connect(t, p, "a@example.com", time.Now().Add(-time.Minute))
	p.exchange = func(context.Context, *http.Client, string) (oauth.Result, error) {
		return oauth.Result{}, errors.New("503 from token endpoint")
	}

	err := p.Refresh(ctx, id)
	if err == nil || errors.Is(err, ErrNeedsReauth) {
		t.Fatalf("Refresh = %v, want the transient error", err)
	}
	a, _ := st.Account(ctx, id)
	if a.RefreshDead() {
		t.Error("a transient failure marked the refresh token dead")
	}
	if a.LastError != "503 from token endpoint" {
		t.Errorf("LastError = %q", a.LastError)
	}
}

// A successful refresh is proof the account works, so it ends any cooldown
// the failures before it had earned.
func TestRefreshSuccessClearsCooldown(t *testing.T) {
	p, _ := fixture(t)
	id := connect(t, p, "a@example.com", time.Now().Add(-time.Minute))
	p.ReportFailure(id, FailureAuth, "401")
	p.exchange = newTokens("new", nil)

	if err := p.Refresh(context.Background(), id); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	p.mu.Lock()
	h := p.states[id]
	cleared := h.cooldownUntil.IsZero() && h.failures == 0
	p.mu.Unlock()
	if !cleared {
		t.Error("cooldown survived a successful refresh")
	}
}

// Refreshing cannot extend the refresh window; only a human can. Inside the
// same three days the client warns in, the gateway has to warn too.
func TestRefreshWarnsWhenReauthIsNear(t *testing.T) {
	p, _ := fixture(t)
	var buf bytes.Buffer
	p.log = slog.New(slog.NewTextHandler(&buf, nil))
	id := connect(t, p, "a@example.com", time.Now().Add(-time.Minute))

	p.exchange = func(context.Context, *http.Client, string) (oauth.Result, error) {
		return oauth.Result{
			AccessToken: "a", RefreshToken: "r",
			ExpiresAt:             time.Now().Add(time.Hour),
			RefreshTokenExpiresAt: time.Now().Add(48 * time.Hour),
		}, nil
	}
	if err := p.Refresh(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "needs re-authorisation soon") {
		t.Errorf("no warning with two days left:\n%s", buf.String())
	}

	buf.Reset()
	p.exchange = func(context.Context, *http.Client, string) (oauth.Result, error) {
		return oauth.Result{
			AccessToken: "a", RefreshToken: "r",
			ExpiresAt:             time.Now().Add(time.Hour),
			RefreshTokenExpiresAt: time.Now().Add(30 * 24 * time.Hour),
		}, nil
	}
	if err := p.Refresh(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "needs re-authorisation soon") {
		t.Errorf("warned with a month left:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "reauth_in") {
		t.Errorf("the refresh window was not logged:\n%s", buf.String())
	}
}

// If the new tokens cannot be stored the refresh has failed, whatever the
// upstream said: reporting success would hand out a token that is not saved.
func TestRefreshFailsWhenTheTokensCannotBeStored(t *testing.T) {
	p, st := fixture(t)
	id := connect(t, p, "a@example.com", time.Now().Add(-time.Minute))
	p.exchange = func(context.Context, *http.Client, string) (oauth.Result, error) {
		// The account vanishes between the exchange and the write.
		if err := st.DeleteAccount(context.Background(), id); err != nil {
			t.Error(err)
		}
		return oauth.Result{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	if err := p.Refresh(context.Background(), id); !errors.Is(err, store.ErrAccountNotFound) {
		t.Errorf("Refresh = %v, want ErrAccountNotFound", err)
	}
}

// A refresh for an account that does not exist must not reach the provider.
func TestRefreshUnknownAccount(t *testing.T) {
	p, _ := fixture(t)
	if err := p.Refresh(context.Background(), "nope"); !errors.Is(err, store.ErrAccountNotFound) {
		t.Errorf("Refresh = %v, want ErrAccountNotFound", err)
	}
}

// A waiter is a request, and its client may leave: it stops waiting at once,
// while the refresh it was waiting on carries on for everybody else.
func TestRefreshWaiterHonoursItsOwnContext(t *testing.T) {
	p, _ := fixture(t)
	id := connect(t, p, "a@example.com", time.Now().Add(-time.Minute))

	started := make(chan struct{})
	release := make(chan struct{})
	p.exchange = func(context.Context, *http.Client, string) (oauth.Result, error) {
		close(started)
		<-release
		return oauth.Result{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}

	var wg sync.WaitGroup
	first := make(chan error, 1)
	wg.Add(1)
	go func() { defer wg.Done(); first <- p.Refresh(context.Background(), id) }()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Refresh(ctx, id); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled waiter got %v, want context.Canceled", err)
	}

	close(release)
	wg.Wait()
	if err := <-first; err != nil {
		t.Errorf("the refresh itself failed: %v", err)
	}
}
