package admin

import (
	"context"
	"time"
)

// How much of the Claude subscription each API key has used.
//
// Anthropic reports a utilisation per account and window — the 5-hour and the
// 7-day — and nothing per caller. So a key's part of it is an estimate: on
// each account, the key's share of what this gateway sent that account in the
// window's current period, times the account's utilisation. Shares are of
// weighted tokens (see store.KeyWeights), because the limits track cost.
//
// Two things it cannot know, and the UI says so. Anything else using the same
// account — the operator's own Claude app, another gateway — is folded into
// the keys that did use it here, so the figures overstate when an account is
// shared. And the weights are the price list's ratios, not Anthropic's limit
// arithmetic, which is not published.

// SubscriptionShare is one key's estimated use of the subscription.
type SubscriptionShare struct {
	// FiveHour and SevenDay are percent of the whole pool's capacity in that
	// window: the per-account figures summed and divided by the accounts that
	// report the window. One full account of two is 50. FiveHour is absent
	// when no account reports a 5-hour window — a subscription without a
	// session limit — rather than a 0 that reads as unused.
	FiveHour *float64 `json:"five_hour,omitempty"`
	SevenDay float64  `json:"seven_day"`
	// Accounts is where it came from, for the key's own page.
	Accounts []AccountShare `json:"accounts"`
	// Providers is the same per provider: the share of the pool of Claude
	// accounts and of the pool of ChatGPT ones, each over its own accounts,
	// since the two subscriptions are separate budgets.
	Providers []ProviderShare `json:"providers"`
}

// ProviderShare is one key's estimated use of one provider's pooled
// subscription, in percent of that pool, as SubscriptionShare's figures are.
type ProviderShare struct {
	Provider string `json:"provider"`
	// FiveHour is absent when none of the provider's accounts reports a
	// 5-hour window.
	FiveHour *float64 `json:"five_hour,omitempty"`
	SevenDay float64  `json:"seven_day"`
	Accounts int      `json:"accounts"`
}

// AccountShare is one key on one account.
type AccountShare struct {
	AccountID string `json:"account_id"`
	Email     string `json:"email"`
	// Name is what the account is called: its name, or its address.
	Name     string `json:"name"`
	Provider string `json:"provider"`
	// FiveHour and SevenDay are percentage points of this account's window;
	// FiveHour is absent when the account reports no 5-hour window.
	FiveHour *float64 `json:"five_hour,omitempty"`
	SevenDay float64  `json:"seven_day"`
	// The key's fraction of this gateway's weighted traffic on the account in
	// each window, which the points above are this fraction of.
	FiveHourShare float64 `json:"five_hour_share"`
	SevenDayShare float64 `json:"seven_day_share"`
}

// accountWindows is what the estimate needs from one account.
type accountWindows struct {
	id, email, name  string
	provider         string
	util5, util7     float64
	weight5, weight7 map[string]float64
	// has5 is whether the account reports a 5-hour window at all.
	has5 bool
}

// ShareOf is one client's estimated share of the subscriptions now, as its
// page shows it, for the MCP server's status tool (internal/httpapi/agent),
// to which the composition root hands it: the surfaces do not import one
// another. ok is false when there is no usage history to estimate from.
func (s *Admin) ShareOf(ctx context.Context, keyID string) (SubscriptionShare, bool) {
	if !s.cfg.Usage.Enabled() {
		return SubscriptionShare{}, false
	}
	windows, err := s.subscriptionWindows(ctx)
	if err != nil {
		s.log.Warn("subscription shares unavailable", "err", err)
		return SubscriptionShare{}, false
	}
	return shareOf(keyID, windows), true
}

// shareOf is the arithmetic, apart from the store so it can be checked
// against numbers worked by hand.
func shareOf(keyID string, accounts []accountWindows) SubscriptionShare {
	out := SubscriptionShare{Accounts: []AccountShare{}, Providers: []ProviderShare{}}
	if len(accounts) == 0 {
		return out
	}
	// Each window over the accounts that report it: a subscription with no
	// session limit is no part of the 5-hour pool, not an idle part of it.
	type pool struct {
		accounts, with5 int
		five, seven     float64
	}
	pools := map[string]*pool{}
	var order []string
	with5 := 0
	for _, a := range accounts {
		p := pools[a.provider]
		if p == nil {
			p = &pool{}
			pools[a.provider] = p
			order = append(order, a.provider)
		}
		p.accounts++
		if a.has5 {
			p.with5++
			with5++
		}
	}
	var five, seven float64
	for _, a := range accounts {
		f5 := fraction(a.weight5, keyID)
		f7 := fraction(a.weight7, keyID)
		if f5 == 0 && f7 == 0 {
			continue
		}
		s := AccountShare{
			AccountID: a.id, Email: a.email, Name: a.name, Provider: a.provider,
			FiveHourShare: f5, SevenDayShare: f7, SevenDay: f7 * a.util7,
		}
		if a.has5 {
			s.FiveHour = ptr(f5 * a.util5)
			five += *s.FiveHour
			pools[a.provider].five += *s.FiveHour
		}
		seven += s.SevenDay
		pools[a.provider].seven += s.SevenDay
		out.Accounts = append(out.Accounts, s)
	}
	if with5 > 0 {
		out.FiveHour = ptr(five / float64(with5))
	}
	out.SevenDay = seven / float64(len(accounts))
	for _, name := range order {
		p := pools[name]
		ps := ProviderShare{Provider: name, Accounts: p.accounts, SevenDay: p.seven / float64(p.accounts)}
		if p.with5 > 0 {
			ps.FiveHour = ptr(p.five / float64(p.with5))
		}
		out.Providers = append(out.Providers, ps)
	}
	return out
}

func ptr(v float64) *float64 { return &v }

func fraction(weights map[string]float64, keyID string) float64 {
	var total float64
	for _, w := range weights {
		total += w
	}
	if total <= 0 {
		return 0
	}
	return weights[keyID] / total
}

// windowStart is when an account's current period began: its reset less the
// window's length when the reset is known and ahead, else the window back from
// now.
func windowStart(reset time.Time, length time.Duration, now time.Time) time.Time {
	if !reset.IsZero() && reset.After(now) {
		return reset.Add(-length)
	}
	return now.Add(-length)
}

// subscriptionWindows reads, for every account that reports a utilisation,
// what each key sent it in its current 5-hour and 7-day periods.
func (s *Admin) subscriptionWindows(ctx context.Context) ([]accountWindows, error) {
	accounts, err := s.store.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var out []accountWindows
	for _, a := range accounts {
		q := a.Quota
		if !q.Known() {
			continue
		}
		w := accountWindows{id: a.ID, email: a.Email, name: a.DisplayName(), provider: a.Provider,
			util5: max(q.FiveHourUtil, 0), util7: max(q.SevenDayUtil, 0), has5: q.FiveHourUtil >= 0}
		if w.weight5, err = s.store.KeyWeights(ctx, a.ID, windowStart(q.FiveHourReset, 5*time.Hour, now)); err != nil {
			return nil, err
		}
		if w.weight7, err = s.store.KeyWeights(ctx, a.ID, windowStart(q.SevenDayReset, 7*24*time.Hour, now)); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}
