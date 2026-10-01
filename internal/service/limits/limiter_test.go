package limits

import (
	"testing"
	"time"
)

// peek must not consume, or it would be indistinguishable from allow.
func TestPeekDoesNotSpendAToken(t *testing.T) {
	l := NewLimiter()
	for range 100 {
		if !l.Peek("ip", 3) {
			t.Fatal("peek refused on a bucket nothing has spent from")
		}
	}
	for i := range 3 {
		if !l.AllowPerMinute("ip", 3) {
			t.Fatalf("allow refused at %d, want the full budget still available", i)
		}
	}
	if l.AllowPerMinute("ip", 3) {
		t.Error("allow granted a fourth token from a budget of 3")
	}
	if l.Peek("ip", 3) {
		t.Error("peek reported room in an exhausted bucket")
	}
}

// The whole reason the period is stored rather than divided away in the
// browser: "two hundred an hour" is not "three a minute". The first permits a
// burst of ten in twenty seconds and then runs dry; the second refuses the
// fourth request.
func TestAPeriodIsNotTheSameAsADividedRate(t *testing.T) {
	burst := func(count int, period time.Duration) int {
		l := NewLimiter()
		start := time.Now()
		l.now = func() time.Time { return start }
		allowed := 0
		for range 10 {
			if l.Allow("k", count, period) {
				allowed++
			}
		}
		return allowed
	}

	// 200 an hour: the whole allowance is there at once.
	if got := burst(200, time.Hour); got != 10 {
		t.Errorf("200/hour allowed %d of 10 immediate requests, want 10", got)
	}
	// The same limit divided down to a per-minute rate refuses most of them,
	// which is the behaviour a UI-side conversion would have shipped.
	if got := burst(3, time.Minute); got != 3 {
		t.Errorf("3/minute allowed %d of 10 immediate requests, want 3", got)
	}
}

// And the allowance really does refill over the period it names.
func TestTheBucketRefillsOverItsPeriod(t *testing.T) {
	l := NewLimiter()
	start := time.Now()
	now := start
	l.now = func() time.Time { return now }

	// Spend all of a small hourly allowance.
	for range 10 {
		if !l.Allow("k", 10, time.Hour) {
			t.Fatal("the initial allowance was not available")
		}
	}
	if l.Allow("k", 10, time.Hour) {
		t.Fatal("an eleventh request was allowed")
	}

	// Six minutes is a tenth of an hour, so one token is back.
	now = start.Add(6 * time.Minute)
	if !l.Allow("k", 10, time.Hour) {
		t.Error("no token had refilled after a tenth of the period")
	}
	if l.Allow("k", 10, time.Hour) {
		t.Error("more than one token refilled in a tenth of the period")
	}
}

func TestLimiterRefills(t *testing.T) {
	l := NewLimiter()
	base := time.Now()
	l.now = func() time.Time { return base }

	// 60/min = 1/s, burst 60. Drain it.
	for i := 0; i < 60; i++ {
		if !l.AllowPerMinute("k", 60) {
			t.Fatalf("request %d denied while the bucket should still be full", i)
		}
	}
	if l.AllowPerMinute("k", 60) {
		t.Fatal("expected denial once the bucket is empty")
	}

	l.now = func() time.Time { return base.Add(2 * time.Second) }
	if !l.AllowPerMinute("k", 60) {
		t.Error("expected a refill after 2s")
	}
}

func TestLimiterDisabledAtZero(t *testing.T) {
	l := NewLimiter()
	for i := 0; i < 1000; i++ {
		if !l.AllowPerMinute("k", 0) {
			t.Fatal("a limit of 0 must disable limiting")
		}
	}
}

func TestLimiterSweepEvictsIdle(t *testing.T) {
	l := NewLimiter()
	base := time.Now()
	l.now = func() time.Time { return base }
	l.AllowPerMinute("k", 60)

	l.now = func() time.Time { return base.Add(time.Hour) }
	l.Sweep(10 * time.Minute)

	l.mu.Lock()
	n := len(l.buckets)
	l.mu.Unlock()
	if n != 0 {
		t.Errorf("len(buckets) = %d, want 0 after sweep", n)
	}
}
