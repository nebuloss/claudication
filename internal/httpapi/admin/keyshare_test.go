package admin

import (
	"math"
	"testing"
	"time"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// Worked by hand. Two accounts: A at 60% of its 5-hour window, with key k1
// three quarters of what this gateway sent it; B at 20%, all of it k2. k1 has
// used 45 points of A's window — half an account's worth of the 5-hour pool
// is 50, so 45/2 = 22.5% of the pool.
func TestShareOfWorkedByHand(t *testing.T) {
	accounts := []accountWindows{
		{id: "a", email: "a@example.com", util5: 60, util7: 30,
			weight5: map[string]float64{"k1": 300, "k2": 100},
			weight7: map[string]float64{"k1": 100, "k2": 100}},
		{id: "b", email: "b@example.com", util5: 20, util7: 10,
			weight5: map[string]float64{"k2": 50},
			weight7: map[string]float64{"k2": 50}},
	}

	k1 := shareOf("k1", accounts)
	if !near(k1.FiveHour, 22.5) || !near(k1.SevenDay, 7.5) {
		t.Errorf("k1 pool share: 5h %v 7d %v, want 22.5 and 7.5", k1.FiveHour, k1.SevenDay)
	}
	if len(k1.Accounts) != 1 || k1.Accounts[0].AccountID != "a" ||
		!near(k1.Accounts[0].FiveHour, 45) || !near(k1.Accounts[0].FiveHourShare, 0.75) {
		t.Errorf("k1 by account: %+v", k1.Accounts)
	}

	k2 := shareOf("k2", accounts)
	if !near(k2.FiveHour, (15+20)/2.0) || len(k2.Accounts) != 2 {
		t.Errorf("k2: %+v", k2)
	}

	// The keys between them account for the whole of what the accounts
	// report: nothing is lost or counted twice.
	if !near(k1.FiveHour+k2.FiveHour, (60+20)/2.0) {
		t.Errorf("shares do not add up to the pool: %v + %v", k1.FiveHour, k2.FiveHour)
	}

	if idle := shareOf("k3", accounts); idle.FiveHour != 0 || len(idle.Accounts) != 0 {
		t.Errorf("a key that sent nothing: %+v", idle)
	}
	if none := shareOf("k1", nil); none.FiveHour != 0 || none.Accounts == nil {
		t.Errorf("no accounts: %+v", none)
	}
}

func TestWindowStartFollowsTheReset(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	reset := now.Add(2 * time.Hour)
	if got := windowStart(reset, 5*time.Hour, now); !got.Equal(now.Add(-3 * time.Hour)) {
		t.Errorf("with a reset ahead: %v", got)
	}
	// Unknown, or already past: the window back from now.
	if got := windowStart(time.Time{}, 5*time.Hour, now); !got.Equal(now.Add(-5 * time.Hour)) {
		t.Errorf("without a reset: %v", got)
	}
	if got := windowStart(now.Add(-time.Minute), 5*time.Hour, now); !got.Equal(now.Add(-5 * time.Hour)) {
		t.Errorf("with a reset passed: %v", got)
	}
}
