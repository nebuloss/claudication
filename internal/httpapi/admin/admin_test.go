package admin

import (
	"testing"
	"time"

	"claudication/internal/oauth"
	"claudication/internal/provider"
	"claudication/internal/store"
)

// An account whose refresh token has a known end has to say how many days are
// left, rounded up the way the client's banner rounds, so the operator sees
// the same number in both places.
func TestAccountJSONCountsReauthDaysUp(t *testing.T) {
	now := time.Now()
	used := now.Add(-time.Minute)
	j := toAccountJSON(store.Account{
		ID: "x", Provider: "anthropic", Email: "e", ExpiresAt: now.Add(time.Hour), CreatedAt: now,
		RefreshExpiresAt: now.Add(36 * time.Hour), LastRefreshAt: &used, LastUsedAt: &used,
		Quota: store.AccountQuota{FiveHourUtil: -1, SevenDayUtil: -1},
	}, nil)
	if j.ReauthDaysLeft == nil || *j.ReauthDaysLeft != 2 {
		t.Errorf("reauth_days_left = %v, want 2 for a day and a half", j.ReauthDaysLeft)
	}
	if j.RefreshExpiresAt == "" || j.LastRefreshAt == "" || j.LastUsedAt == "" {
		t.Errorf("timestamps missing: %+v", j)
	}
	if j.Quota != nil {
		t.Error("an unknown quota (-1) was reported as known")
	}
}

// Filter parsing: repeated values dedupe, an empty value is a real filter for
// "nothing in this column", and an unreadable status code is dropped rather
// than failing the page.
func TestFilterParsing(t *testing.T) {
	if cleanSet(nil) != nil {
		t.Error("absent parameter narrowed")
	}
	if got := cleanSet([]string{"a", "a", ""}); len(got) != 2 || got[1] != "" {
		t.Errorf("cleanSet = %q", got)
	}
	if got := codeSet([]string{"x", "y"}); got != nil {
		t.Errorf("codeSet of nonsense = %v, want no narrowing", got)
	}
	if got := codeSet([]string{"500", "500", "429"}); len(got) != 2 {
		t.Errorf("codeSet = %v", got)
	}
	for in, want := range map[string]string{"failed": "failed", "ok": "ok", "OK": "", "": ""} {
		if got := outcomeOf(in); got != want {
			t.Errorf("outcomeOf(%q) = %q", in, got)
		}
	}
}

// byAddress is a provider whose account_uuid does not say who signed in.
type byAddress struct{ provider.Accounts }

func (byAddress) IdentifiedByAddress() {}

// Google's account_uuid is the project it bills, shared by every free
// account and changed by a plan change: two addresses on it are two
// accounts, and a reconnect after an upgrade is still the same one.
func TestAddressIdentityIgnoresTheProject(t *testing.T) {
	stored := store.Account{Email: "a@example.com", AccountUUID: "aicode-consumers"}
	google := byAddress{}
	other := oauth.Result{Email: "b@example.com", AccountUUID: "aicode-consumers"}
	if sameAccount(stored, identity(google, other), other.Email) {
		t.Error("a second address on the shared project was taken for the first account")
	}
	upgraded := oauth.Result{Email: "A@example.com", AccountUUID: "paid-project-1"}
	if !sameAccount(stored, identity(google, upgraded), upgraded.Email) {
		t.Error("a reconnect on a new project was refused as another account")
	}
	if got := identity(nil, upgraded); got != "paid-project-1" {
		t.Errorf("identity without AddressIdentity = %q, want the provider's id", got)
	}
}
