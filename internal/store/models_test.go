package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// A dated id and its alias are one model: both spellings are in the request
// log, and a switch that covered one would leave the model reachable by the
// other.
func TestModelKey(t *testing.T) {
	for in, want := range map[string]string{
		"claude-haiku-4-5-20251001": "claude-haiku-4-5",
		"claude-haiku-4-5":          "claude-haiku-4-5",
		" Claude-Opus-5-5 ":         "claude-opus-5-5",
		"claude-opus-4-5-2025":      "claude-opus-4-5-2025", // not a date
	} {
		if got := ModelKey(in); got != want {
			t.Errorf("ModelKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// Off is the decision; on is the default. A model turned off under one
// spelling is off under the other, turning it on under either clears it, and
// the switches go with their account.
func TestAccountModelSwitches(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	sealer := testSealer(t)
	a, err := st.UpsertAccount(ctx, sealer, Account{Provider: "anthropic", Email: "a@x", ExpiresAt: time.Now().Add(time.Hour)},
		Tokens{AccessToken: "a", RefreshToken: "r"})
	if err != nil {
		t.Fatal(err)
	}
	if !a.Serves("claude-opus-5-5") || !a.Serves("") || a.ModelsOff != nil {
		t.Fatalf("a fresh account: %+v", a)
	}

	if err := st.SetAccountModel(ctx, a.ID, "claude-haiku-4-5-20251001", false); err != nil {
		t.Fatal(err)
	}
	// Twice, and under the alias: still one row.
	for _, m := range []string{"claude-haiku-4-5-20251001", "claude-haiku-4-5"} {
		if err := st.SetAccountModel(ctx, a.ID, m, false); err != nil {
			t.Fatal(err)
		}
	}
	a, _ = st.Account(ctx, a.ID)
	if !slices.Equal(a.ModelsOff, []string{"claude-haiku-4-5-20251001"}) {
		t.Errorf("models off = %v, want one row as switched", a.ModelsOff)
	}
	if a.Serves("claude-haiku-4-5") || a.Serves("claude-haiku-4-5-20251001") || !a.Serves("claude-opus-5") {
		t.Error("the switch does not cover both spellings, or covers another model")
	}

	// The list carries it too, which is what the pool reads.
	all, _ := st.ListAccounts(ctx)
	if len(all) != 1 || len(all[0].ModelsOff) != 1 {
		t.Errorf("listed account = %+v", all)
	}

	if err := st.SetAccountModel(ctx, a.ID, "claude-haiku-4-5", true); err != nil {
		t.Fatal(err)
	}
	if a, _ = st.Account(ctx, a.ID); a.ModelsOff != nil {
		t.Errorf("turning the alias on left %v off", a.ModelsOff)
	}

	if err := st.SetAccountModel(ctx, "nope", "m", false); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("unknown account: %v", err)
	}
	if err := st.SetAccountModel(ctx, a.ID, " ", false); err == nil {
		t.Error("an empty model was switched")
	}

	// Gone with the account.
	if err := st.SetAccountModel(ctx, a.ID, "claude-opus-5", false); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_models_off`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d switches outlived their account (%v)", n, err)
	}
}
