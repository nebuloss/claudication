package httpapi

import (
	"testing"
	"time"

	"claudication/internal/store"
)

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
