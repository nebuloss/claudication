package store

import (
	"context"
	"testing"
	"time"
)

// The cross-tabs are the margins the report already had, crossed. They come
// out of the same grouped pass, so the thing worth pinning is that the folding
// agrees with the breakdowns it sits beside.
func TestUsageCrossTabs(t *testing.T) {
	st := usageStore(t)
	ctx := context.Background()
	day1 := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	day2 := day1.Add(24 * time.Hour)

	record(t, st, UsageEvent{At: day1, Model: "claude-opus-5", KeyID: "k1", KeyName: "laptop",
		AccountEmail: "a@example.com", Status: 200, InputTokens: 100, OutputTokens: 10})
	record(t, st, UsageEvent{At: day1, Model: "claude-opus-5", KeyID: "k1", KeyName: "laptop",
		AccountEmail: "a@example.com", Status: 400, Error: "refused"})
	record(t, st, UsageEvent{At: day1, Model: "claude-haiku-4-5", KeyID: "k2", KeyName: "ci",
		AccountEmail: "a@example.com", Status: 200, InputTokens: 5})
	record(t, st, UsageEvent{At: day2, Model: "claude-opus-5", KeyID: "k3", KeyName: "laptop",
		AccountEmail: "b@example.com", Status: 200, OutputTokens: 7})

	rep, err := st.Usage(ctx, day1.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	for _, dimension := range []string{CrossModel, CrossKey, CrossAccount, CrossStatus} {
		if len(rep.Cross[dimension]) == 0 {
			t.Errorf("%s cross-tab is empty", dimension)
		}
	}

	find := func(dimension, day, name string) UsageCell {
		for _, c := range rep.Cross[dimension] {
			if c.Day == day && c.Name == name {
				return c
			}
		}
		t.Fatalf("no %s cell for %s/%s", dimension, day, name)
		return UsageCell{}
	}

	opus1 := find(CrossModel, "2026-09-10", "claude-opus-5")
	if opus1.Requests != 2 || opus1.Errors != 1 {
		t.Errorf("opus on day 1 = %d requests, %d errors; want 2 and 1", opus1.Requests, opus1.Errors)
	}
	if opus1.InputTokens != 100 || opus1.OutputTokens != 10 {
		t.Errorf("opus tokens = %d in, %d out", opus1.InputTokens, opus1.OutputTokens)
	}

	// Two different keys used the same name — one minted, used and deleted,
	// then another. They are two entities and one line on a chart, so the
	// cross-tab folds them; three lines all labelled "laptop" say nothing.
	laptopDays := 0
	for _, c := range rep.Cross[CrossKey] {
		if c.Name == "laptop" {
			laptopDays++
		}
	}
	if laptopDays != 2 {
		t.Errorf("the two keys named laptop folded into %d rows, want one per day", laptopDays)
	}

	// The status breakdown has never carried token counts, and neither may its
	// cross-tab — the UI disables the tokens control on that tab because of it.
	for _, c := range rep.Cross[CrossStatus] {
		if c.InputTokens != 0 || c.OutputTokens != 0 || c.CacheTokens != 0 {
			t.Errorf("status cell %s/%s carries tokens", c.Day, c.Name)
		}
	}

	// Each cross-tab has to add up to the margin beside it, or the two halves
	// of one screen disagree.
	for _, tc := range []struct {
		dimension string
		buckets   []UsageBucket
	}{
		{CrossModel, rep.ByModel},
		{CrossKey, rep.ByKey},
		{CrossAccount, rep.ByAccount},
		{CrossStatus, rep.ByStatus},
	} {
		var crossed, margin int64
		for _, c := range rep.Cross[tc.dimension] {
			crossed += c.Requests
		}
		for _, b := range tc.buckets {
			margin += b.Requests
		}
		if crossed != margin {
			t.Errorf("%s: cross-tab totals %d, breakdown totals %d", tc.dimension, crossed, margin)
		}
		if crossed != rep.Totals.Requests {
			t.Errorf("%s: cross-tab totals %d, report totals %d",
				tc.dimension, crossed, rep.Totals.Requests)
		}
	}

	// Oldest day first, then by name: a chart that reshuffles between
	// refreshes looks like something happened.
	previous := UsageCell{}
	for _, c := range rep.Cross[CrossModel] {
		if c.Day < previous.Day || (c.Day == previous.Day && c.Name < previous.Name) {
			t.Errorf("cross-tab out of order at %s/%s", c.Day, c.Name)
		}
		previous = c
	}
}
