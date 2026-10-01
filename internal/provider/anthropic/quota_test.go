package anthropic

import (
	"strings"
	"testing"
	"time"
)

func strp(s string) *string { return &s }

func TestQuotaFromUsage(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	u := AccountUsage{
		FiveHour:  &UsageWindow{Utilization: 29, ResetsAt: strp("2026-10-01T14:00:00.123+02:00")},
		SevenDay:  &UsageWindow{Utilization: 45, LockedReason: strp("rate_limited")},
		Limits:    []UsageLimit{{Kind: "session", Percent: 29}},
		FetchedAt: at,
	}
	q := u.Quota()
	if q.UpdatedAt != at || q.FiveHourUtil != 29 || q.SevenDayUtil != 45 {
		t.Errorf("figures: %+v", q)
	}
	if want := time.Date(2026, 10, 1, 12, 0, 0, 123_000_000, time.UTC); !q.FiveHourReset.Equal(want) {
		t.Errorf("reset = %s, want %s in UTC", q.FiveHourReset, want)
	}
	if q.FiveHourStatus != "allowed" || q.SevenDayStatus != "rate_limited" {
		t.Errorf("statuses %q / %q", q.FiveHourStatus, q.SevenDayStatus)
	}
	if !strings.Contains(q.Detail, `"kind":"session"`) {
		t.Errorf("limits not passed through: %s", q.Detail)
	}
	if !q.Known() {
		t.Error("a reported quota reads as unknown")
	}
}

// A window the endpoint left out is unknown, not idle.
func TestQuotaMarksMissingWindowsUnknown(t *testing.T) {
	q := AccountUsage{}.Quota()
	if q.FiveHourUtil != -1 || q.SevenDayUtil != -1 || q.Known() {
		t.Errorf("missing windows: %+v", q)
	}
}

func TestParseUsageTimeToleratesNothingAndNonsense(t *testing.T) {
	for _, v := range []*string{nil, strp(""), strp("yesterday")} {
		if got := parseUsageTime(v); !got.IsZero() {
			t.Errorf("%v parsed as %s", v, got)
		}
	}
	if lockedOrAllowed(strp("")) != "allowed" || lockedOrAllowed(nil) != "allowed" {
		t.Error("an empty locked reason is not allowed")
	}
}
