package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"claudication/internal/httpapi/admin"
	"claudication/internal/pool"
	"claudication/internal/store"
)

// The fake's code exchange always answers as new@example.com, uuid-1.

type reconnectResult struct {
	Account admin.AccountJSON `json:"account"`
	Renewed bool              `json:"renewed"`
}

// startLogin begins a consent flow, for accountID or for a new account, and
// returns its state and authorize URL.
func startLogin(t *testing.T, base string, c *http.Cookie, accountID string) (state string, authURL *url.URL) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"account_id": accountID})
	status, body := call(t, base, http.MethodPost, "/admin/accounts/oauth/start", string(b), c)
	if status != http.StatusOK {
		t.Fatalf("start: %d %s", status, body)
	}
	got := decode[struct {
		State     string `json:"state"`
		AuthURL   string `json:"auth_url"`
		AccountID string `json:"account_id"`
	}](t, body)
	if got.AccountID != accountID {
		t.Errorf("start answered for account %q, want %q", got.AccountID, accountID)
	}
	u, err := url.Parse(got.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	return got.State, u
}

func finishLogin(t *testing.T, base string, c *http.Cookie, state string) (int, []byte) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"state": state, "redirect_url": "good#" + state})
	return call(t, base, http.MethodPost, "/admin/accounts/oauth/complete", string(b), c)
}

func accountCount(t *testing.T, st *store.Store) int {
	t.Helper()
	all, err := st.ListAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(all)
}

// Reconnecting is what an account whose refresh token died needs, and it has
// to fix that account rather than add another: same id, same place in the
// priority list, its pause and its dead token cleared — and the pool's
// backoff, earned by the old credentials, forgotten so it serves at once.
func TestReconnectRenewsTheAccountInPlace(t *testing.T) {
	srv, st, _ := newTestServer(t)
	fake := &fakeAnthropic{}
	fake.install(srv)
	base, c := adminAPI(t, srv)
	ctx := context.Background()

	addAccount(t, srv, st, "first@example.com", "a1", "r1", time.Now().Add(time.Hour))
	dead := addAccount(t, srv, st, "new@example.com", "old-access", "dead-refresh", time.Now().Add(-time.Hour))
	if err := st.MarkRefreshDead(ctx, dead.ID, "invalid_grant"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountDisabled(ctx, dead.ID, true); err != nil {
		t.Fatal(err)
	}
	srv.pool.ReportFailure(dead.ID, pool.FailureAuth, "invalid_grant")

	state, authURL := startLogin(t, base, c, dead.ID)
	if got := authURL.Query().Get("login_hint"); got != "new@example.com" {
		t.Errorf("login_hint = %q, want the account's own address", got)
	}

	status, body := finishLogin(t, base, c, state)
	if status != http.StatusOK {
		t.Fatalf("complete: %d %s", status, body)
	}
	got := decode[reconnectResult](t, body)
	if !got.Renewed || got.Account.ID != dead.ID {
		t.Errorf("renewed=%v id=%s; want the same account renewed", got.Renewed, got.Account.ID)
	}
	// Its place in the priority list is the list's to report.
	_, listed := call(t, base, http.MethodGet, "/admin/accounts", "", c)
	if l := decode[accountsList](t, listed).Accounts; len(l) != 2 || l[1].ID != dead.ID {
		t.Errorf("after a reconnect the list is %+v; want it second, where it was", l)
	}
	if got.Account.NeedsReauth || got.Account.Disabled || got.Account.LastError != "" {
		t.Errorf("after a reconnect: needs_reauth=%v disabled=%v last_error=%q",
			got.Account.NeedsReauth, got.Account.Disabled, got.Account.LastError)
	}
	if n := accountCount(t, st); n != 2 {
		t.Errorf("%d accounts after a reconnect, want 2", n)
	}
	tokens, err := st.AccountTokens(ctx, srv.sealer, dead.ID)
	if err != nil || tokens.RefreshToken != "new-refresh" {
		t.Errorf("stored tokens = %+v, %v", tokens, err)
	}
	stored, _ := st.Account(ctx, dead.ID)
	if stored.AccountUUID != "uuid-1" {
		t.Errorf("account_uuid = %q, want it learned from the consent", stored.AccountUUID)
	}
	ps, err := srv.pool.Status(ctx, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if _, cooling := ps.Cooling[dead.ID]; cooling {
		t.Error("the reconnected account is still cooling down from its old credentials")
	}
}

// The browser approves as whichever Claude account it is signed in to, login
// hint or not. A reconnect that comes back as someone else changes nothing,
// says what happened and how to fix it, and hands the stray grant back.
func TestReconnectAsSomeoneElseChangesNothing(t *testing.T) {
	srv, st, _ := newTestServer(t)
	fake := &fakeAnthropic{}
	fake.install(srv)
	base, c := adminAPI(t, srv)
	ctx := context.Background()

	mine := addAccount(t, srv, st, "mine@example.com", "my-access", "my-refresh", time.Now().Add(time.Hour))

	state, _ := startLogin(t, base, c, mine.ID)
	status, body := finishLogin(t, base, c, state)
	if status != http.StatusConflict || errType(t, body) != "wrong_account" {
		t.Fatalf("complete: %d %s, want 409 wrong_account", status, body)
	}
	for _, want := range []string{"new@example.com", "mine@example.com", "private window"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the refusal does not mention %q: %s", want, body)
		}
	}
	if n := accountCount(t, st); n != 1 {
		t.Errorf("%d accounts, want 1: the other account was added", n)
	}
	tokens, _ := st.AccountTokens(ctx, srv.sealer, mine.ID)
	if tokens.RefreshToken != "my-refresh" {
		t.Errorf("the account's own tokens were replaced: %+v", tokens)
	}
	if !slices.Contains(fake.revokedTokens(), "new-refresh") {
		t.Errorf("revoked = %v, want the stray grant handed back", fake.revokedTokens())
	}
	// The attempt is spent either way: its code has been redeemed.
	if status, body := finishLogin(t, base, c, state); errType(t, body) != "unknown_state" {
		t.Errorf("retrying a spent attempt: %d %s", status, body)
	}
}

// The provider's account id outlives an address. An account whose email has
// changed upstream is still recognised, and takes its new address.
func TestReconnectMatchesByAccountIDOverAddress(t *testing.T) {
	srv, st, _ := newTestServer(t)
	fake := &fakeAnthropic{}
	fake.install(srv)
	base, c := adminAPI(t, srv)

	renamed, err := st.UpsertAccount(context.Background(), srv.sealer,
		store.Account{Provider: "anthropic", Email: "old-name@example.com", AccountUUID: "uuid-1",
			ExpiresAt: time.Now().Add(time.Hour)},
		store.Tokens{AccessToken: "a", RefreshToken: "r"})
	if err != nil {
		t.Fatal(err)
	}

	state, _ := startLogin(t, base, c, renamed.ID)
	status, body := finishLogin(t, base, c, state)
	if status != http.StatusOK {
		t.Fatalf("complete: %d %s", status, body)
	}
	got := decode[reconnectResult](t, body).Account
	if got.ID != renamed.ID || got.Email != "new@example.com" {
		t.Errorf("account = %s %s, want %s under its new address", got.ID, got.Email, renamed.ID)
	}

	// The other way round: same address, a different account id upstream, is
	// a different account.
	other, err := st.UpsertAccount(context.Background(), srv.sealer,
		store.Account{Provider: "anthropic", Email: "twin@example.com", AccountUUID: "uuid-other",
			ExpiresAt: time.Now().Add(time.Hour)},
		store.Tokens{AccessToken: "a2", RefreshToken: "r2"})
	if err != nil {
		t.Fatal(err)
	}
	state, _ = startLogin(t, base, c, other.ID)
	if status, body := finishLogin(t, base, c, state); status != http.StatusConflict {
		t.Errorf("a different account id was accepted: %d %s", status, body)
	}
}

// Adding an account that is already connected used to be the only way to
// renew one, and it still is a renewal: no second row, the same id.
func TestAddingAnAccountAgainRenewsIt(t *testing.T) {
	srv, st, _ := newTestServer(t)
	fake := &fakeAnthropic{}
	fake.install(srv)
	base, c := adminAPI(t, srv)

	existing := addAccount(t, srv, st, "new@example.com", "old", "old-refresh", time.Now().Add(time.Hour))

	state, authURL := startLogin(t, base, c, "")
	if authURL.Query().Has("login_hint") {
		t.Errorf("a new-account login carries a hint: %s", authURL)
	}
	status, body := finishLogin(t, base, c, state)
	if status != http.StatusOK {
		t.Fatalf("complete: %d %s", status, body)
	}
	got := decode[reconnectResult](t, body)
	if !got.Renewed || got.Account.ID != existing.ID {
		t.Errorf("renewed=%v id=%s, want the existing %s renewed", got.Renewed, got.Account.ID, existing.ID)
	}
	if n := accountCount(t, st); n != 1 {
		t.Errorf("%d accounts, want 1", n)
	}
}

// A reconnect names an account that has to exist when it starts, and still
// exist when it finishes; if it was removed in between, the grant goes back.
func TestReconnectOfAnAccountThatIsGone(t *testing.T) {
	srv, st, _ := newTestServer(t)
	fake := &fakeAnthropic{}
	fake.install(srv)
	base, c := adminAPI(t, srv)

	if status, body := call(t, base, http.MethodPost, "/admin/accounts/oauth/start",
		`{"account_id":"nope"}`, c); status != http.StatusNotFound {
		t.Errorf("start for an unknown account: %d %s", status, body)
	}

	acct := addAccount(t, srv, st, "new@example.com", "a", "r", time.Now().Add(time.Hour))
	state, _ := startLogin(t, base, c, acct.ID)
	if err := st.DeleteAccount(context.Background(), acct.ID); err != nil {
		t.Fatal(err)
	}
	status, body := finishLogin(t, base, c, state)
	if status != http.StatusNotFound {
		t.Errorf("complete after removal: %d %s", status, body)
	}
	if n := accountCount(t, st); n != 0 {
		t.Errorf("%d accounts: a removed account came back", n)
	}
	if !slices.Contains(fake.revokedTokens(), "new-refresh") {
		t.Errorf("revoked = %v, want the grant handed back", fake.revokedTokens())
	}
}
