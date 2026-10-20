package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// ledgerName is what history is labelled with for an account.
func ledgerName(t *testing.T, st *Store, id string) string {
	t.Helper()
	var name string
	if err := st.db.QueryRow(`SELECT name FROM names WHERE kind = 'account' AND id = ?`, id).Scan(&name); err != nil {
		t.Fatalf("names row for %s: %v", id, err)
	}
	return name
}

// An account is called its address until it is named, its name once it is,
// and its address again once the name is cleared — in its row and in the
// ledger history is labelled from, which must never disagree.
func TestSetAccountNameSetsAndClears(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	a := addAccount(t, st, "a@example.com")

	if a.Name != "" || a.DisplayName() != "a@example.com" || ledgerName(t, st, a.ID) != "a@example.com" {
		t.Fatalf("a new account: name %q, display %q, ledger %q", a.Name, a.DisplayName(), ledgerName(t, st, a.ID))
	}

	if err := st.SetAccountName(ctx, a.ID, "  Work seat  "); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Account(ctx, a.ID)
	if got.Name != "Work seat" || got.DisplayName() != "Work seat" || got.Email != "a@example.com" {
		t.Errorf("named: %+v", got)
	}
	if n := ledgerName(t, st, a.ID); n != "Work seat" {
		t.Errorf("ledger after naming = %q", n)
	}

	if err := st.SetAccountName(ctx, a.ID, "   "); err != nil {
		t.Fatal(err)
	}
	got, _ = st.Account(ctx, a.ID)
	if got.Name != "" || got.DisplayName() != "a@example.com" {
		t.Errorf("cleared: %+v", got)
	}
	if n := ledgerName(t, st, a.ID); n != "a@example.com" {
		t.Errorf("ledger after clearing = %q, want the address back", n)
	}

	if err := st.SetAccountName(ctx, "nope", "x"); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("unknown account: %v", err)
	}
}

// Two accounts may share a name: the id is the identity, and the address
// still tells them apart wherever the UI shows both.
func TestAccountNamesNeedNotBeUnique(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	a := addAccount(t, st, "a@example.com")
	b := addAccount(t, st, "b@example.com")
	for _, id := range []string{a.ID, b.ID} {
		if err := st.SetAccountName(ctx, id, "Team"); err != nil {
			t.Fatalf("name %s: %v", id, err)
		}
	}
	list, _ := st.ListAccounts(ctx)
	for _, x := range list {
		if x.Name != "Team" {
			t.Errorf("%s: name %q", x.Email, x.Name)
		}
	}
}

// The rule is the one a client's name follows: one line, a bounded length.
// A refused name changes nothing.
func TestAccountNameValidation(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	a := addAccount(t, st, "a@example.com")
	if err := st.SetAccountName(ctx, a.ID, "Kept"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Repeat("x", MaxNameLen+1),
		"two\nlines",
		"tab\there",
		"\xff\xfe",
	} {
		if err := st.SetAccountName(ctx, a.ID, bad); !errors.Is(err, ErrInvalidName) {
			t.Errorf("%q: %v, want ErrInvalidName", bad, err)
		}
	}
	// The limit is characters, not bytes: a name in another script is as long
	// as it looks.
	if err := st.SetAccountName(ctx, a.ID, strings.Repeat("é", MaxNameLen)); err != nil {
		t.Errorf("%d two-byte characters: %v", MaxNameLen, err)
	}
	if err := st.SetAccountName(ctx, a.ID, "Kept"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Account(ctx, a.ID); got.Name != "Kept" {
		t.Errorf("name after refusals = %q", got.Name)
	}

	// The same rule holds for a client's (a key's) name.
	k, _, err := st.CreateKey(ctx, "k", KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateKey(ctx, k.ID, "two\nlines", KeyLimits{}); !errors.Is(err, ErrInvalidName) {
		t.Errorf("key rename across lines: %v", err)
	}
	if _, _, err := st.CreateKey(ctx, strings.Repeat("x", MaxNameLen+1), KeyLimits{}); !errors.Is(err, ErrInvalidName) {
		t.Errorf("over-long key name: %v", err)
	}
}

// A sign-in again keeps the operator's name; a new address renames an unnamed
// account's history and leaves a named one's alone.
func TestAccountNameSurvivesReconnect(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	a := addAccount(t, st, "a@example.com")
	b := addAccount(t, st, "b@example.com")
	if err := st.SetAccountName(ctx, a.ID, "Personal"); err != nil {
		t.Fatal(err)
	}

	again := addAccount(t, st, "a@example.com")
	if again.ID != a.ID || again.Name != "Personal" || ledgerName(t, st, a.ID) != "Personal" {
		t.Errorf("after signing in again: %+v, ledger %q", again, ledgerName(t, st, a.ID))
	}

	renew := Account{Email: "a2@example.com", ExpiresAt: time.Now().Add(time.Hour)}
	if _, err := st.RenewAccount(ctx, a.ID, renew, Tokens{AccessToken: "x", RefreshToken: "y"}); err != nil {
		t.Fatal(err)
	}
	if n := ledgerName(t, st, a.ID); n != "Personal" {
		t.Errorf("named account after a new address: ledger %q", n)
	}
	renew.Email = "b2@example.com"
	if _, err := st.RenewAccount(ctx, b.ID, renew, Tokens{AccessToken: "x", RefreshToken: "y"}); err != nil {
		t.Fatal(err)
	}
	if n := ledgerName(t, st, b.ID); n != "b2@example.com" {
		t.Errorf("unnamed account after a new address: ledger %q", n)
	}
}
