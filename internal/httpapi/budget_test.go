package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"claudication/internal/service/limits"
	"claudication/internal/store"
)

// spend files usage against a key as if requests had been served.
func spend(t *testing.T, st *store.Store, keyID string, tokens int, at time.Time) {
	t.Helper()
	if err := st.RecordUsage(context.Background(), store.UsageEvent{
		At: at, KeyID: keyID, KeyName: "test",
		Model: "claude-haiku-4-5", Path: "/v1/messages", Status: 200,
		InputTokens: tokens,
	}); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
}

// The promise the column made and never kept: a key with a budget stops being
// served once it has spent it.
func TestATokenBudgetIsEnforced(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	key, plaintext, err := st.CreateKey(context.Background(), "capped", store.KeyLimits{TokenBudget: 1000})
	if err != nil {
		t.Fatal(err)
	}

	get := func() (int, string) {
		req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
		req.Header.Set("X-Api-Key", plaintext)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Retry-After")
	}

	// Under budget: not refused for the budget's sake. There is no upstream in
	// this fixture, so the request fails later — what matters is that it is not
	// a 429.
	if status, _ := get(); status == http.StatusTooManyRequests {
		t.Fatal("a key that has spent nothing was refused")
	}

	// Now spend it.
	spend(t, st, key.ID, 1200, time.Now().Add(-time.Hour))
	srv.budgets.Forget(key.ID)

	status, retryAfter := get()
	if status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 once the budget is spent", status)
	}
	if retryAfter == "" {
		t.Error("no Retry-After: the client is left guessing when the budget frees up")
	}
}

// Unlimited has to stay possible, and be the default.
func TestAnUnlimitedKeyIsNeverRefusedForItsBudget(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	// 0 is unlimited, and is what CreateKey is given when nobody chooses.
	key, plaintext, err := st.CreateKey(context.Background(), "open", store.KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if key.TokenBudget != 0 {
		t.Fatalf("token budget = %d, want 0 for unlimited", key.TokenBudget)
	}

	// Far more than any budget would allow.
	spend(t, st, key.ID, 500_000_000, time.Now().Add(-time.Hour))
	srv.budgets.Forget(key.ID)

	req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
	req.Header.Set("X-Api-Key", plaintext)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		t.Error("an unlimited key was refused for its budget")
	}
}

// Spending outside the window does not count: the whole point of a rolling
// budget is that it frees up rather than latching.
func TestSpendOutsideTheWindowDoesNotCount(t *testing.T) {
	_, st, _ := newTestServer(t)
	ctx := context.Background()

	key, _, err := st.CreateKey(ctx, "rolling", store.KeyLimits{TokenBudget: 1000})
	if err != nil {
		t.Fatal(err)
	}
	spend(t, st, key.ID, 5000, time.Now().Add(-limits.BudgetWindow-time.Hour))
	spend(t, st, key.ID, 100, time.Now().Add(-time.Minute))

	got, err := st.KeySpend(ctx, key.ID, time.Now().Add(-limits.BudgetWindow))
	if err != nil {
		t.Fatal(err)
	}
	if got != 100 {
		t.Errorf("spend in window = %d, want 100; older spending must age out", got)
	}
}

// One key's spending is not another's.
func TestBudgetsArePerKey(t *testing.T) {
	_, st, _ := newTestServer(t)
	ctx := context.Background()

	a, _, err := st.CreateKey(ctx, "a", store.KeyLimits{TokenBudget: 1000})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := st.CreateKey(ctx, "b", store.KeyLimits{TokenBudget: 1000})
	if err != nil {
		t.Fatal(err)
	}
	spend(t, st, a.ID, 900, time.Now())

	if got, _ := st.KeySpend(ctx, a.ID, time.Now().Add(-limits.BudgetWindow)); got != 900 {
		t.Errorf("key a spend = %d, want 900", got)
	}
	if got, _ := st.KeySpend(ctx, b.ID, time.Now().Add(-limits.BudgetWindow)); got != 0 {
		t.Errorf("key b spend = %d, want 0; a neighbour's traffic must not count", got)
	}
}

// A budget that cannot be read must not become a gateway that refuses
// everything: bounding spend is not worth turning a storage fault into an
// outage.
func TestAnUnreadableBudgetFailsOpen(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	_, plaintext, err := st.CreateKey(context.Background(), "broken", store.KeyLimits{TokenBudget: 1000})
	if err != nil {
		t.Fatal(err)
	}
	// Close the store out from under the server: every query now fails.
	st.Close()

	req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
	req.Header.Set("X-Api-Key", plaintext)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		t.Error("a budget that could not be read refused the request")
	}
}
