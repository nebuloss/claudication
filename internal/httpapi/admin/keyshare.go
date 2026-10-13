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
	// report a utilisation. One full account of two is 50.
	FiveHour float64 `json:"five_hour"`
	SevenDay float64 `json:"seven_day"`
	// Accounts is where it came from, for the key's own page.
	Accounts []AccountShare `json:"accounts"`
}

// AccountShare is one key on one account.
type AccountShare struct {
	AccountID string `json:"account_id"`
	Email     string `json:"email"`
	// FiveHour and SevenDay are percentage points of this account's window.
	FiveHour float64 `json:"five_hour"`
	SevenDay float64 `json:"seven_day"`
	// The key's fraction of this gateway's weighted traffic on the account in
	// each window, which the points above are this fraction of.
	FiveHourShare float64 `json:"five_hour_share"`
	SevenDayShare float64 `json:"seven_day_share"`
}

// accountWindows is what the estimate needs from one account.
type accountWindows struct {
	id, email        string
	util5, util7     float64
	weight5, weight7 map[string]float64
}

// shareOf is the arithmetic, apart from the store so it can be checked
// against numbers worked by hand.
func shareOf(keyID string, accounts []accountWindows) SubscriptionShare {
	out := SubscriptionShare{Accounts: []AccountShare{}}
	if len(accounts) == 0 {
		return out
	}
	for _, a := range accounts {
		f5 := fraction(a.weight5, keyID)
		f7 := fraction(a.weight7, keyID)
		if f5 == 0 && f7 == 0 {
			continue
		}
		s := AccountShare{
			AccountID: a.id, Email: a.email,
			FiveHourShare: f5, SevenDayShare: f7,
			FiveHour: f5 * a.util5, SevenDay: f7 * a.util7,
		}
		out.FiveHour += s.FiveHour
		out.SevenDay += s.SevenDay
		out.Accounts = append(out.Accounts, s)
	}
	out.FiveHour /= float64(len(accounts))
	out.SevenDay /= float64(len(accounts))
	return out
}

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
		w := accountWindows{id: a.ID, email: a.Email, util5: max(q.FiveHourUtil, 0), util7: max(q.SevenDayUtil, 0)}
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
