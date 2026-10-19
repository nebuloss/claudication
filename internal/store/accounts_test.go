package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"claudication/internal/secret"
)

func testSealer(t *testing.T) *secret.Sealer {
	t.Helper()
	// An inherited key would make the test depend on the shell it runs in.
	t.Setenv(secret.KeyEnv, "")
	s, err := secret.Load(t.TempDir())
	if err != nil {
		t.Fatalf("secret.Load: %v", err)
	}
	return s
}

func addAccount(t *testing.T, st *Store, email string) Account {
	t.Helper()
	a, err := st.UpsertAccount(context.Background(),
		Account{Provider: "anthropic", Email: email, AccountUUID: "uuid-" + email,
			ExpiresAt: time.Now().Add(time.Hour)},
		Tokens{AccessToken: "at-" + email, RefreshToken: "rt-" + email})
	if err != nil {
		t.Fatalf("UpsertAccount(%s): %v", email, err)
	}
	return a
}

// A new account must arrive with its quota unknown rather than idle, and at
// the end of the priority list so adding one never changes who is serving.
func TestUpsertAccountNewAccount(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	a := addAccount(t, st, "a@example.com")
	b := addAccount(t, st, "b@example.com")

	if a.ID == "" || a.ID == b.ID {
		t.Fatalf("ids %q and %q, want two distinct generated ids", a.ID, b.ID)
	}
	if a.Position != 0 || b.Position != 1 {
		t.Errorf("positions %d,%d; want 0,1", a.Position, b.Position)
	}
	if a.Quota.Known() {
		t.Errorf("fresh account quota is Known: %+v", a.Quota)
	}
	if u := a.Quota.Utilization(); u != -1 {
		t.Errorf("Utilization = %v, want -1 for unknown", u)
	}
	if a.AccountUUID != "uuid-a@example.com" || a.Provider != "anthropic" {
		t.Errorf("identity not stored: %+v", a)
	}
	if a.LastRefreshAt == nil || a.CreatedAt.IsZero() {
		t.Errorf("timestamps not set: created %v, refreshed %v", a.CreatedAt, a.LastRefreshAt)
	}
	if !a.RefreshExpiresAt.IsZero() {
		t.Errorf("RefreshExpiresAt = %v, want zero when the provider did not say", a.RefreshExpiresAt)
	}

	tok, err := st.AccountTokens(ctx, a.ID)
	if err != nil {
		t.Fatalf("AccountTokens: %v", err)
	}
	if tok.AccessToken != "at-a@example.com" || tok.RefreshToken != "rt-a@example.com" {
		t.Errorf("tokens = %+v, want the ones stored", tok)
	}

	// Sealed at rest: the database alone must not hand out a credential.
	var raw []byte
	if err := st.DB().QueryRowContext(ctx,
		`SELECT access_token FROM accounts WHERE id = ?`, a.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if string(raw) == "at-a@example.com" {
		t.Error("access token stored in the clear")
	}
}

// Logging in again is the human intervention every failure state was waiting
// for, so it must clear them — but keep the account's identity and place.
func TestUpsertAccountReloginClearsFailureState(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	first := addAccount(t, st, "a@example.com")
	addAccount(t, st, "b@example.com")

	st.MarkAccountError(ctx, first.ID, "  boom  ")
	if err := st.MarkRefreshDead(ctx, first.ID, "invalid_grant"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountDisabled(ctx, first.ID, true); err != nil {
		t.Fatal(err)
	}

	refreshBy := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second)
	again, err := st.UpsertAccount(ctx,
		Account{ID: "ignored", Provider: "anthropic", Email: "a@example.com", AccountUUID: "uuid-a@example.com",
			ExpiresAt: time.Now().Add(time.Hour), RefreshExpiresAt: refreshBy},
		Tokens{AccessToken: "new-at", RefreshToken: "new-rt"})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID {
		t.Errorf("relogin changed id %q -> %q", first.ID, again.ID)
	}
	if again.Position != first.Position {
		t.Errorf("relogin moved the account from %d to %d", first.Position, again.Position)
	}
	if again.Disabled() || again.RefreshDead() || again.LastError != "" {
		t.Errorf("failure state survived relogin: %+v", again)
	}
	if !again.RefreshExpiresAt.Equal(refreshBy) {
		t.Errorf("RefreshExpiresAt = %v, want %v", again.RefreshExpiresAt, refreshBy)
	}
	tok, err := st.AccountTokens(ctx, first.ID)
	if err != nil || tok.AccessToken != "new-at" || tok.RefreshToken != "new-rt" {
		t.Errorf("tokens after relogin = %+v, %v", tok, err)
	}
	list, err := st.ListAccounts(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListAccounts = %d, %v; want 2 rows, the relogin not duplicating", len(list), err)
	}
}

// A refresh that does not report a new refresh deadline must not erase the
// one already known; losing it would hide the reauth warning.
func TestUpdateAccountTokensKeepsKnownRefreshExpiry(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	refreshBy := time.Now().Add(10 * 24 * time.Hour).UTC().Truncate(time.Second)
	a, err := st.UpsertAccount(ctx,
		Account{Provider: "anthropic", Email: "a@example.com",
			ExpiresAt: time.Now(), RefreshExpiresAt: refreshBy},
		Tokens{AccessToken: "at", RefreshToken: "rt"})
	if err != nil {
		t.Fatal(err)
	}
	st.MarkAccountError(ctx, a.ID, "refresh failed")

	expires := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	if err := st.UpdateAccountTokens(ctx, a.ID,
		Tokens{AccessToken: "at2", RefreshToken: "rt2"}, expires, time.Time{}); err != nil {
		t.Fatalf("UpdateAccountTokens: %v", err)
	}
	got, err := st.Account(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.RefreshExpiresAt.Equal(refreshBy) {
		t.Errorf("RefreshExpiresAt = %v, want the earlier %v kept", got.RefreshExpiresAt, refreshBy)
	}
	if !got.ExpiresAt.Equal(expires) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, expires)
	}
	if got.LastError != "" {
		t.Errorf("LastError = %q, want cleared by a successful refresh", got.LastError)
	}
	tok, _ := st.AccountTokens(ctx, a.ID)
	if tok.AccessToken != "at2" || tok.RefreshToken != "rt2" {
		t.Errorf("tokens = %+v", tok)
	}

	// A new deadline, when given, replaces the old one.
	later := refreshBy.Add(24 * time.Hour)
	if err := st.UpdateAccountTokens(ctx, a.ID,
		Tokens{AccessToken: "at3", RefreshToken: "rt3"}, expires, later); err != nil {
		t.Fatal(err)
	}
	got, _ = st.Account(ctx, a.ID)
	if !got.RefreshExpiresAt.Equal(later) {
		t.Errorf("RefreshExpiresAt = %v, want %v", got.RefreshExpiresAt, later)
	}

	if err := st.UpdateAccountTokens(ctx, "nope", Tokens{}, expires, later); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("UpdateAccountTokens(unknown) = %v, want ErrAccountNotFound", err)
	}
}

// "Since when" is what the operator needs, so a repeated invalid_grant must
// not move the first time forward.
func TestMarkRefreshDeadKeepsTheFirstTime(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	a := addAccount(t, st, "a@example.com")

	if err := st.MarkRefreshDead(ctx, a.ID, "first"); err != nil {
		t.Fatal(err)
	}
	old := "2020-01-02T03:04:05Z"
	oldAt, _ := time.Parse(time.RFC3339, old)
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE accounts SET refresh_dead_at = ? WHERE id = ?`, oldAt.UnixMilli(), a.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkRefreshDead(ctx, a.ID, "second"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Account(ctx, a.ID)
	if got.RefreshDeadAt == nil || got.RefreshDeadAt.Format(time.RFC3339) != old {
		t.Errorf("RefreshDeadAt = %v, want the first %s", got.RefreshDeadAt, old)
	}
	if got.LastError != "second" {
		t.Errorf("LastError = %q, want the latest detail", got.LastError)
	}
	if !got.NeedsReauth() {
		t.Error("a dead refresh token must need reauth")
	}
}

// Reordering takes the whole list: unknown ids are ignored and an unlisted
// account keeps its place after the listed ones rather than among them.
func TestSetAccountOrder(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	a := addAccount(t, st, "a@example.com")
	b := addAccount(t, st, "b@example.com")
	c := addAccount(t, st, "c@example.com")

	if err := st.SetAccountOrder(ctx, []string{c.ID, "ghost", a.ID}); err != nil {
		t.Fatalf("SetAccountOrder: %v", err)
	}
	list, err := st.ListAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range list {
		got = append(got, x.Email)
	}
	want := []string{"c@example.com", "a@example.com", "b@example.com"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("order = %v, want %v", got, want)
	}
	if b2, _ := st.Account(ctx, b.ID); b2.Position <= 2 {
		t.Errorf("unlisted account position %d, want after the listed ones", b2.Position)
	}

	// A new account still lands last after a reorder.
	d := addAccount(t, st, "d@example.com")
	list, _ = st.ListAccounts(ctx)
	if list[len(list)-1].ID != d.ID {
		t.Errorf("new account not last: %v", list[len(list)-1].Email)
	}
}

// The quota poll's figures are what routing decides on, so they must round
// trip exactly, including the raw detail passed to the UI.
func TestSetAccountQuotaRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	a := addAccount(t, st, "a@example.com")

	reset5 := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	q := AccountQuota{
		FiveHourUtil: 42.5, FiveHourReset: reset5, FiveHourStatus: "allowed",
		SevenDayUtil: 80, SevenDayStatus: "allowed_warning",
		Detail: `[{"kind":"weekly"}]`,
	}
	if err := st.SetAccountQuota(ctx, a.ID, q); err != nil {
		t.Fatal(err)
	}
	got, err := st.Account(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	gq := got.Quota
	if gq.FiveHourUtil != 42.5 || gq.SevenDayUtil != 80 || gq.Detail != q.Detail {
		t.Errorf("quota = %+v", gq)
	}
	if !gq.FiveHourReset.Equal(reset5) || !gq.SevenDayReset.IsZero() {
		t.Errorf("resets = %v / %v; want %v and zero", gq.FiveHourReset, gq.SevenDayReset, reset5)
	}
	if gq.UpdatedAt.IsZero() {
		t.Error("UpdatedAt not set")
	}
	if !gq.Known() || gq.Utilization() != 80 {
		t.Errorf("Known=%v Utilization=%v, want true and the fuller window 80", gq.Known(), gq.Utilization())
	}
	if gq.Allowed() {
		t.Error("a non-allowed status must not count as allowed")
	}

	// Writing to an account that is gone is not an error: it is a status poll.
	if err := st.SetAccountQuota(ctx, "ghost", q); err != nil {
		t.Errorf("SetAccountQuota(unknown) = %v", err)
	}
}

func TestAccountQuotaHelpers(t *testing.T) {
	// The headline windows decide routing; these are the cases that would
	// misroute if the comparison or the "never said" rule were wrong.
	cases := []struct {
		name    string
		q       AccountQuota
		known   bool
		util    float64
		allowed bool
	}{
		{"unknown", AccountQuota{FiveHourUtil: -1, SevenDayUtil: -1}, false, -1, true},
		{"five hour binds", AccountQuota{FiveHourUtil: 90, SevenDayUtil: 10, FiveHourStatus: "allowed"}, true, 90, true},
		{"seven day binds", AccountQuota{FiveHourUtil: 5, SevenDayUtil: 60}, true, 60, true},
		{"only one known", AccountQuota{FiveHourUtil: -1, SevenDayUtil: 0}, true, 0, true},
		{"rejected", AccountQuota{FiveHourStatus: "rejected", SevenDayStatus: "allowed"}, true, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.q.Known() != c.known || c.q.Utilization() != c.util || c.q.Allowed() != c.allowed {
				t.Errorf("Known=%v Utilization=%v Allowed=%v; want %v %v %v",
					c.q.Known(), c.q.Utilization(), c.q.Allowed(), c.known, c.util, c.allowed)
			}
		})
	}
}

// The reauth helpers drive the operator's warnings; a wrong boundary means
// either a false alarm or an account that silently stops.
func TestAccountReauthHelpers(t *testing.T) {
	now := time.Now()
	dead := now
	cases := []struct {
		name               string
		a                  Account
		known, needs, soon bool
	}{
		{"unknown window", Account{}, false, false, false},
		{"far away", Account{RefreshExpiresAt: now.Add(30 * 24 * time.Hour)}, true, false, false},
		{"inside three days", Account{RefreshExpiresAt: now.Add(48 * time.Hour)}, true, false, true},
		{"closed", Account{RefreshExpiresAt: now.Add(-time.Minute)}, true, true, true},
		{"dead token", Account{RefreshDeadAt: &dead}, false, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, known := c.a.RefreshWindow()
			if known != c.known || c.a.NeedsReauth() != c.needs || c.a.NeedsReauthSoon() != c.soon {
				t.Errorf("known=%v needs=%v soon=%v; want %v %v %v",
					known, c.a.NeedsReauth(), c.a.NeedsReauthSoon(), c.known, c.needs, c.soon)
			}
		})
	}

	if !(Account{ExpiresAt: now.Add(-time.Second)}).Expired() {
		t.Error("past expiry not Expired")
	}
	if (Account{ExpiresAt: now.Add(time.Hour)}).Expired() {
		t.Error("future expiry Expired")
	}
}

// Disabling must be reversible without the browser flow, and use must clear
// a stale error so the badge reflects the account's current state.
func TestAccountDisableUseAndDelete(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	a := addAccount(t, st, "a@example.com")

	if err := st.SetAccountDisabled(ctx, a.ID, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Account(ctx, a.ID); !got.Disabled() {
		t.Error("not disabled")
	}
	if err := st.SetAccountDisabled(ctx, a.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Account(ctx, a.ID); got.Disabled() {
		t.Error("still disabled after re-enable")
	}
	if err := st.SetAccountDisabled(ctx, "ghost", true); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("SetAccountDisabled(unknown) = %v", err)
	}

	st.MarkAccountError(ctx, a.ID, "  upstream 500 \n")
	if got, _ := st.Account(ctx, a.ID); got.LastError != "upstream 500" {
		t.Errorf("LastError = %q, want trimmed", got.LastError)
	}
	st.MarkAccountUsed(ctx, a.ID)
	got, _ := st.Account(ctx, a.ID)
	if got.LastUsedAt == nil || got.LastError != "" {
		t.Errorf("after use: LastUsedAt=%v LastError=%q", got.LastUsedAt, got.LastError)
	}

	if err := st.DeleteAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteAccount(ctx, a.ID); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("second DeleteAccount = %v", err)
	}
	if _, err := st.Account(ctx, a.ID); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("Account after delete = %v", err)
	}
	if _, err := st.AccountByEmail(ctx, "anthropic", "a@example.com"); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("AccountByEmail after delete = %v", err)
	}
	if _, err := st.AccountTokens(ctx, a.ID); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("AccountTokens after delete = %v", err)
	}
}

// A sealing key that changed (a lost secret.key, a wrong env var) must surface
// as an error, never as garbage tokens sent upstream.
func TestAccountTokensWithTheWrongKey(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(ctx, path, testSealer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := addAccount(t, st, "a@example.com")

	other, err := Open(ctx, path, testSealer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.AccountTokens(ctx, a.ID); err == nil {
		t.Error("AccountTokens with another key succeeded")
	}
}

// An account is its provider's account id as much as its address: one ChatGPT
// sign-in reaches a personal workspace and a Team one under the same address,
// and they are two accounts. One stored before ids were — an older release's
// Anthropic row — is still found by its address.
func TestAccountByIdentity(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	add := func(uuid, plan string) Account {
		t.Helper()
		a, err := st.UpsertAccount(ctx, Account{Provider: "openai", Email: "a@example.com", AccountUUID: uuid,
			Plan: plan, ExpiresAt: time.Now().Add(time.Hour)}, Tokens{AccessToken: "at", RefreshToken: "rt"})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	personal, team := add("acct-personal", "plus"), add("acct-team", "team")
	if personal.ID == team.ID || personal.Plan != "plus" || team.Plan != "team" {
		t.Fatalf("two workspaces = %+v and %+v", personal, team)
	}
	if got, err := st.AccountByIdentity(ctx, "openai", "acct-team", "a@example.com"); err != nil || got.ID != team.ID {
		t.Errorf("by id = %s, %v; want the team workspace", got.ID, err)
	}
	if _, err := st.AccountByIdentity(ctx, "openai", "acct-other", "a@example.com"); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("a third workspace = %v, want not found", err)
	}
	// A sign-in again keeps the plan it last said when it says none.
	again := add("acct-team", "")
	if again.ID != team.ID || again.Plan != "team" {
		t.Errorf("signed in again = %+v", again)
	}

	legacy, err := st.UpsertAccount(ctx, Account{Provider: "anthropic", Email: "old@example.com",
		ExpiresAt: time.Now().Add(time.Hour)}, Tokens{AccessToken: "at", RefreshToken: "rt"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.AccountByIdentity(ctx, "anthropic", "uuid-now-known", "old@example.com"); err != nil || got.ID != legacy.ID {
		t.Errorf("an account stored without its id = %s, %v; want it found by address", got.ID, err)
	}
}
