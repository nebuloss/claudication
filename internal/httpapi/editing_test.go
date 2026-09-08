package httpapi

import (
	"context"
	"testing"

	"claudication/internal/store"
)

// Renaming a key must not disturb the secret. The credential is the expensive
// thing to change — every client holding it has to be visited — so a typo in a
// label should not cost that.
func TestUpdatingAKeyLeavesTheSecretAlone(t *testing.T) {
	_, st, _ := newTestServer(t)
	ctx := context.Background()

	key, plaintext, err := st.CreateKey(ctx, "laptp", store.KeyLimits{RPMLimit: 120})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateKey(ctx, key.ID, "laptop", store.KeyLimits{RPMLimit: 240, TokenBudget: 5000}); err != nil {
		t.Fatalf("UpdateKey: %v", err)
	}

	got, err := st.Authenticate(ctx, plaintext)
	if err != nil {
		t.Fatalf("the key stopped working after a rename: %v", err)
	}
	if got.Name != "laptop" {
		t.Errorf("name = %q, want laptop", got.Name)
	}
	if got.RPMLimit != 240 {
		t.Errorf("rpm_limit = %d, want 240", got.RPMLimit)
	}
	if got.TokenBudget != 5000 {
		t.Errorf("token_budget = %d, want 5000", got.TokenBudget)
	}
	if got.ID != key.ID {
		t.Errorf("id changed: %q -> %q", key.ID, got.ID)
	}
}

func TestUpdatingAKeyValidates(t *testing.T) {
	_, st, _ := newTestServer(t)
	ctx := context.Background()
	key, _, err := st.CreateKey(ctx, "laptop", store.KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateKey(ctx, key.ID, "   ", store.KeyLimits{RPMLimit: 10}); err == nil {
		t.Error("a blank name was accepted")
	}
	if err := st.UpdateKey(ctx, key.ID, "laptop", store.KeyLimits{RPMLimit: -1}); err == nil {
		t.Error("a negative rate limit was accepted")
	}
	if err := st.UpdateKey(ctx, "no-such-key", "laptop", store.KeyLimits{RPMLimit: 10}); err == nil {
		t.Error("updating a key that does not exist succeeded")
	}
}

// disabled_at had a column, a Disabled() helper, a check in the pool and a
// badge in the UI, and nothing that ever set it — so the only way to stop
// using an account was to delete it, which revokes its token upstream.
func TestAnAccountCanBePausedAndResumed(t *testing.T) {
	srv, st, _ := newTestServer(t)
	ctx := context.Background()

	if err := seedAccount(t, st, srv); err != nil {
		t.Fatal(err)
	}
	accounts, err := st.ListAccounts(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("ListAccounts: %v, %d accounts", err, len(accounts))
	}
	id := accounts[0].ID

	// It starts in rotation.
	if status, err := srv.pool.Status(ctx, "anthropic"); err != nil || status.Serving != id {
		t.Fatalf("a fresh account should be serving: %v, %+v", err, status)
	}

	if err := st.SetAccountDisabled(ctx, id, true); err != nil {
		t.Fatalf("SetAccountDisabled(true): %v", err)
	}
	one, err := st.Account(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !one.Disabled() {
		t.Error("the account did not come back disabled")
	}
	status, err := srv.pool.Status(ctx, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if status.Serving != "" {
		t.Errorf("a paused account is still serving: %+v", status)
	}
	if status.Usable != 0 {
		t.Errorf("usable = %d, want 0", status.Usable)
	}

	// And the credentials survived, so resuming needs no browser.
	if _, err := st.AccountTokens(ctx, srv.sealer, id); err != nil {
		t.Errorf("pausing destroyed the credentials: %v", err)
	}

	if err := st.SetAccountDisabled(ctx, id, false); err != nil {
		t.Fatalf("SetAccountDisabled(false): %v", err)
	}
	if status, err := srv.pool.Status(ctx, "anthropic"); err != nil || status.Serving != id {
		t.Errorf("resuming did not put the account back: %v, %+v", err, status)
	}
}

func TestPausingAnAccountThatDoesNotExist(t *testing.T) {
	_, st, _ := newTestServer(t)
	if err := st.SetAccountDisabled(context.Background(), "nope", true); err == nil {
		t.Error("pausing an unknown account succeeded")
	} else if err != store.ErrAccountNotFound {
		t.Errorf("err = %v, want ErrAccountNotFound", err)
	}
}
