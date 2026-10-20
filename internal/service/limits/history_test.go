package limits

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

// A burst of ten in one second against five a minute: the bucket starts full
// and lets five through, as the request path would.
func TestReplayRate(t *testing.T) {
	var uses []Use
	for i := 0; i < 10; i++ {
		uses = append(uses, Use{At: t0.Add(time.Duration(i) * 100 * time.Millisecond), Tokens: 1})
	}
	r := ReplayLimits(uses, 5, time.Minute, 0)
	if r.Requests != 10 || r.RateRefused != 5 || r.BudgetRefused != 0 {
		t.Errorf("replay %+v, want 5 of 10 refused by the rate", r)
	}
	// A minute later the bucket is full again.
	uses = append(uses, Use{At: t0.Add(2 * time.Minute), Tokens: 1})
	if r := ReplayLimits(uses, 5, time.Minute, 0); r.RateRefused != 5 {
		t.Errorf("replay %+v: the refilled bucket refused", r)
	}
	if r := ReplayLimits(uses, 0, 0, 0); r.RateRefused != 0 {
		t.Errorf("no rate refused %d", r.RateRefused)
	}
}

// The budget is a rolling day: once the day before a request holds the budget
// it is refused, refusals spend nothing, and a day later it frees up.
func TestReplayBudget(t *testing.T) {
	uses := []Use{
		{At: t0, Tokens: 600},
		{At: t0.Add(time.Hour), Tokens: 500},                // 600 < 1000: accepted, now 1100
		{At: t0.Add(2 * time.Hour), Tokens: 1},              // refused
		{At: t0.Add(3 * time.Hour), Tokens: 1},              // refused
		{At: t0.Add(24*time.Hour + time.Minute), Tokens: 1}, // the 600 aged out: 500 < 1000
		{At: t0.Add(48 * time.Hour), Tokens: 1},
	}
	r := ReplayLimits(uses, 0, 0, 1000)
	if r.BudgetRefused != 2 || r.BudgetDays != 1 {
		t.Errorf("replay %+v, want 2 refused on 1 day", r)
	}
}

func TestSuggest(t *testing.T) {
	now := t0.Add(7 * 24 * time.Hour)
	var uses []Use
	// Twenty busy minutes of 4 requests and one of 30, over three days.
	for d := 0; d < 3; d++ {
		for m := 0; m < 20; m++ {
			for i := 0; i < 4; i++ {
				uses = append(uses, Use{At: t0.Add(time.Duration(d)*24*time.Hour + time.Duration(m)*time.Minute +
					time.Duration(i)*time.Second), Tokens: 1000})
			}
		}
	}
	for i := 0; i < 30; i++ {
		uses = append(uses, Use{At: t0.Add(5*time.Hour + time.Duration(i)*time.Second), Tokens: 10})
	}
	s := Suggest(uses, now, 7)
	// 61 busy minutes, sorted: 60 fours and a thirty; the 95th is a four.
	if s.PeakPerMinute != 30 || s.RatePerMinute != 6 {
		t.Errorf("rate %+v, want p95 4 ×1.5 = 6, peak 30", s)
	}
	// Days: 80 000, 80 300, 80 000; p95 80 300 ×1.25 = 100 375 → 110 000.
	if s.ActiveDays != 3 || s.PeakDaily != 80300 || s.DailyTokens != 110000 {
		t.Errorf("tokens %+v", s)
	}
	if empty := Suggest(nil, now, 7); empty.RatePerMinute != 0 || empty.DailyTokens != 0 {
		t.Errorf("an idle key was suggested %+v", empty)
	}
	// Outside the window counts for nothing.
	if s := Suggest(uses, now.Add(30*24*time.Hour), 7); s.ActiveDays != 0 {
		t.Errorf("old history was read: %+v", s)
	}
}

func TestRoundUp2(t *testing.T) {
	for in, want := range map[int64]int64{0: 0, 99: 99, 100: 100, 101: 110, 123456: 130000, 990001: 1000000} {
		if got := roundUp2(in); got != want {
			t.Errorf("roundUp2(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestInFlight(t *testing.T) {
	f := NewInFlight()
	a, b := f.Begin("k"), f.Begin("k")
	if f.Open("k") != 2 || f.Open("other") != 0 {
		t.Errorf("open %d", f.Open("k"))
	}
	a()
	b()
	if f.Open("k") != 0 {
		t.Errorf("open %d after both closed", f.Open("k"))
	}
}
