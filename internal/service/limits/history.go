package limits

import (
	"math"
	"sort"
	"sync"
	"time"
)

// Use is one request a key made, as its history replays it: when, and the
// tokens it was charged.
type Use struct {
	At     time.Time
	Tokens int64
}

// Replay is what a pair of limits would have refused of a history.
type Replay struct {
	// Requests is the history's length; RateRefused and BudgetRefused the
	// requests each limit would have turned away. A request the rate refused
	// spent nothing, so it is not counted against the budget too.
	Requests      int `json:"requests"`
	RateRefused   int `json:"rate_refused"`
	BudgetRefused int `json:"budget_refused"`
	// BudgetDays is the UTC days on which the budget would have refused one.
	BudgetDays int `json:"budget_days"`
}

// ReplayLimits runs a history, oldest first, through the limits the gateway
// enforces: the rate through the same token bucket the request path uses
// (count per period, 0 for none), the budget as the rolling day it is (0 for
// none), refusing a request once the tokens of the day before it reach the
// budget, as withinBudget does. So the preview an operator is shown before
// saving is the request path's own arithmetic, not a second opinion of it.
func ReplayLimits(uses []Use, count int, period time.Duration, budget int64) Replay {
	out := Replay{Requests: len(uses)}
	var now time.Time
	l := NewLimiter()
	l.now = func() time.Time { return now }
	// The accepted requests inside the rolling day, as a queue.
	var window []Use
	var inWindow int64
	days := map[string]bool{}
	for _, u := range uses {
		now = u.At
		if count > 0 && !l.Allow("replay", count, period) {
			out.RateRefused++
			continue
		}
		if budget > 0 {
			cut := u.At.Add(-BudgetWindow)
			for len(window) > 0 && !window[0].At.After(cut) {
				inWindow -= window[0].Tokens
				window = window[1:]
			}
			if inWindow >= budget {
				out.BudgetRefused++
				days[u.At.UTC().Format("2006-01-02")] = true
				continue
			}
			window = append(window, u)
			inWindow += u.Tokens
		}
	}
	out.BudgetDays = len(days)
	return out
}

// Suggestion is a pair of limits read off a key's own history: high enough
// that its ordinary work never meets them, low enough to stop it running away.
type Suggestion struct {
	// Days is how far back it was read.
	Days int `json:"days"`
	// RatePerMinute is the 95th percentile of its busy minutes with half as
	// much again on top; PeakPerMinute its busiest minute.
	RatePerMinute int `json:"rate_per_minute"`
	PeakPerMinute int `json:"peak_per_minute"`
	// DailyTokens is the 95th percentile of its UTC days' tokens with a
	// quarter on top, rounded up to two significant figures; PeakDaily its
	// busiest day.
	DailyTokens int64 `json:"daily_tokens"`
	PeakDaily   int64 `json:"peak_daily"`
	// ActiveDays is how many of those days it was used at all; a suggestion
	// from one busy afternoon is worth less than one from a month.
	ActiveDays int `json:"active_days"`
}

// Suggest reads limits off the uses since now less days. Only the minutes
// and days with traffic count: a key idle most of the week would otherwise
// have a 95th percentile of nothing.
func Suggest(uses []Use, now time.Time, days int) Suggestion {
	out := Suggestion{Days: days}
	since := now.Add(-time.Duration(days) * 24 * time.Hour)
	perMinute := map[int64]int{}
	perDay := map[string]int64{}
	for _, u := range uses {
		if u.At.Before(since) || u.At.After(now) {
			continue
		}
		perMinute[u.At.Unix()/60]++
		perDay[u.At.UTC().Format("2006-01-02")] += u.Tokens
	}
	if len(perMinute) == 0 {
		return out
	}
	minutes := make([]int, 0, len(perMinute))
	for _, n := range perMinute {
		minutes = append(minutes, n)
	}
	sort.Ints(minutes)
	out.PeakPerMinute = minutes[len(minutes)-1]
	out.RatePerMinute = int(math.Ceil(float64(percentile(minutes, 95)) * 1.5))

	daily := make([]int64, 0, len(perDay))
	for _, t := range perDay {
		daily = append(daily, t)
	}
	sort.Slice(daily, func(i, j int) bool { return daily[i] < daily[j] })
	out.ActiveDays = len(daily)
	out.PeakDaily = daily[len(daily)-1]
	out.DailyTokens = roundUp2(int64(math.Ceil(float64(percentile(daily, 95)) * 1.25)))
	return out
}

// percentile is the nearest-rank percentile of sorted values.
func percentile[T int | int64](sorted []T, p int) T {
	rank := int(math.Ceil(float64(p) / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}

// roundUp2 rounds up to two significant figures: 123 456 is 130 000, which is
// a number an operator can read and type.
func roundUp2(n int64) int64 {
	if n < 100 {
		return n
	}
	scale := int64(1)
	for n/scale >= 100 {
		scale *= 10
	}
	return (n + scale - 1) / scale * scale
}

// InFlight counts the requests each key has open now: what a client is doing
// this second, which no history can say.
type InFlight struct {
	mu   sync.Mutex
	open map[string]int
	// changed is called on each request counted open or closed.
	changed func()
}

// OnChange has fn called on each request counted open or closed from now on.
func (f *InFlight) OnChange(fn func()) { f.changed = fn }

func (f *InFlight) note() {
	if f.changed != nil {
		f.changed()
	}
}

// NewInFlight returns an empty counter.
func NewInFlight() *InFlight { return &InFlight{open: map[string]int{}} }

// Begin counts a request open for key and returns the call that closes it.
func (f *InFlight) Begin(key string) func() {
	f.mu.Lock()
	f.open[key]++
	f.mu.Unlock()
	f.note()
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.open[key]--; f.open[key] <= 0 {
			delete(f.open, key)
		}
		f.note()
	}
}

// Open is how many requests key has open.
func (f *InFlight) Open(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.open[key]
}

// Total is how many requests every key together has open: the gateway's own
// load this second, which Activity shows beside its last hour. Summed here
// rather than kept as a second counter, so the two cannot disagree.
func (f *InFlight) Total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.open {
		n += c
	}
	return n
}
