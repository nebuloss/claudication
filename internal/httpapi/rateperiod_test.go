package httpapi

import (
	"testing"
	"time"

	"claudication/internal/store"
)

// The whole reason the period is stored rather than divided away in the
// browser: "two hundred an hour" is not "three a minute". The first permits a
// burst of ten in twenty seconds and then runs dry; the second refuses the
// fourth request.
func TestAPeriodIsNotTheSameAsADividedRate(t *testing.T) {
	burst := func(count int, period time.Duration) int {
		l := newLimiter()
		start := time.Now()
		l.now = func() time.Time { return start }
		allowed := 0
		for range 10 {
			if l.allow("k", count, period) {
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
	l := newLimiter()
	start := time.Now()
	now := start
	l.now = func() time.Time { return now }

	// Spend all of a small hourly allowance.
	for range 10 {
		if !l.allow("k", 10, time.Hour) {
			t.Fatal("the initial allowance was not available")
		}
	}
	if l.allow("k", 10, time.Hour) {
		t.Fatal("an eleventh request was allowed")
	}

	// Six minutes is a tenth of an hour, so one token is back.
	now = start.Add(6 * time.Minute)
	if !l.allow("k", 10, time.Hour) {
		t.Error("no token had refilled after a tenth of the period")
	}
	if l.allow("k", 10, time.Hour) {
		t.Error("more than one token refilled in a tenth of the period")
	}
}

// A key created before periods existed keeps the behaviour it had.
func TestAKeyWithNoPeriodMeansAMinute(t *testing.T) {
	_, st, _ := newTestServer(t)

	key, _, err := st.CreateKey(t.Context(), "legacy", store.KeyLimits{RPMLimit: 60})
	if err != nil {
		t.Fatal(err)
	}
	if got := key.Period(); got != time.Minute {
		t.Errorf("period = %s, want a minute when none was chosen", got)
	}

	// And it round-trips through the database as a minute rather than zero.
	fetched, err := st.ListKeys(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(fetched) != 1 {
		t.Fatalf("keys = %d, want 1", len(fetched))
	}
	if got := fetched[0].Period(); got != time.Minute {
		t.Errorf("stored period = %s, want a minute", got)
	}
}

// A chosen period survives a round trip and an edit.
func TestARatePeriodRoundTrips(t *testing.T) {
	_, st, _ := newTestServer(t)
	ctx := t.Context()

	key, _, err := st.CreateKey(ctx, "hourly", store.KeyLimits{
		RPMLimit:   200,
		RatePeriod: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if key.Period() != time.Hour {
		t.Errorf("period = %s, want an hour", key.Period())
	}

	got, err := st.ListKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Period() != time.Hour || got[0].RPMLimit != 200 {
		t.Errorf("stored = %d per %s, want 200 per hour", got[0].RPMLimit, got[0].Period())
	}

	if err := st.UpdateKey(ctx, key.ID, "daily", store.KeyLimits{
		RPMLimit:   5000,
		RatePeriod: 24 * time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	got, err = st.ListKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Period() != 24*time.Hour || got[0].RPMLimit != 5000 {
		t.Errorf("after edit = %d per %s, want 5000 per day", got[0].RPMLimit, got[0].Period())
	}
}

func TestARatePeriodIsValidated(t *testing.T) {
	_, st, _ := newTestServer(t)
	if _, _, err := st.CreateKey(t.Context(), "bad", store.KeyLimits{
		RPMLimit:   10,
		RatePeriod: -time.Minute,
	}); err == nil {
		t.Error("a negative period was accepted")
	}
}
