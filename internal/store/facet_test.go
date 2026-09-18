package store

import (
	"context"
	"testing"
	"time"
)

// A set filter is the thing one value could not express, so the test is two
// values at once — with a third row present that must not come back.
func TestRequestFilterKeepsEveryValueInTheSet(t *testing.T) {
	st := usageStore(t)
	ctx := context.Background()
	now := time.Now()

	record(t, st, UsageEvent{At: now.Add(-3 * time.Minute), Model: "sonnet", Status: 200})
	record(t, st, UsageEvent{At: now.Add(-2 * time.Minute), Model: "haiku", Status: 200})
	record(t, st, UsageEvent{At: now.Add(-1 * time.Minute), Model: "opus", Status: 200})

	events, _, err := st.RecentUsage(ctx, 10, UsageCursor{}, RequestFilter{
		Models: []string{"sonnet", "haiku"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("%d rows for two models, want 2", len(events))
	}
	for _, e := range events {
		if e.Model == "opus" {
			t.Errorf("opus came back from a filter that did not name it")
		}
	}
}

// The empty string is a member like any other: a refused request has no model,
// and asking for those is the reason `?model=` has to mean something.
func TestRequestFilterCanAskForTheRowsWithNothing(t *testing.T) {
	st := usageStore(t)
	now := time.Now()

	record(t, st, UsageEvent{At: now.Add(-2 * time.Minute), Model: "sonnet", Status: 200})
	record(t, st, UsageEvent{At: now.Add(-1 * time.Minute), Model: "", Status: 401, Rejected: true})

	events, _, err := st.RecentUsage(context.Background(), 10, UsageCursor{}, RequestFilter{
		Models: []string{""},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Status != 401 {
		t.Fatalf("rows for the empty model = %+v, want the one refused request", events)
	}
}

// Two filters compose with AND, which is what makes "this chat, and only what
// broke" a single question.
func TestRequestFilterSetsCompose(t *testing.T) {
	st := usageStore(t)
	now := time.Now()

	record(t, st, UsageEvent{At: now.Add(-3 * time.Minute), Model: "sonnet", Client: "crush", Status: 200})
	record(t, st, UsageEvent{At: now.Add(-2 * time.Minute), Model: "sonnet", Client: "Codex", Status: 200})
	record(t, st, UsageEvent{At: now.Add(-1 * time.Minute), Model: "haiku", Client: "crush", Status: 200})

	events, _, err := st.RecentUsage(context.Background(), 10, UsageCursor{}, RequestFilter{
		Models:  []string{"sonnet"},
		Clients: []string{"crush"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("%d rows, want the one that is both", len(events))
	}
	if events[0].Model != "sonnet" || events[0].Client != "crush" {
		t.Errorf("got %s/%s, want sonnet/crush", events[0].Model, events[0].Client)
	}
}

// What the menus offer: every value the column holds, commonest first, counted
// over everything rather than over a page.
func TestRequestFacetsCountTheWholeLog(t *testing.T) {
	st := usageStore(t)
	now := time.Now()

	for i := 0; i < 3; i++ {
		record(t, st, UsageEvent{
			At:    now.Add(-time.Duration(i) * time.Minute),
			Model: "sonnet", Client: "crush", IP: "10.0.0.1", Status: 200,
		})
	}
	record(t, st, UsageEvent{At: now.Add(-9 * time.Minute), Model: "haiku", Client: "Codex", IP: "10.0.0.2", Status: 429})
	record(t, st, UsageEvent{At: now.Add(-8 * time.Minute), Model: "", Client: "curl", IP: "10.0.0.3", Status: 0})

	facets, err := st.RequestFacets(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}

	if len(facets.Models) != 3 {
		t.Fatalf("%d models, want sonnet, haiku and the empty one", len(facets.Models))
	}
	// Commonest first, which is the order a menu wants.
	if facets.Models[0].Value != "sonnet" || facets.Models[0].Count != 3 {
		t.Errorf("first model = %+v, want sonnet with 3", facets.Models[0])
	}
	// The rows with nothing are offered, because filtering to them is allowed.
	var sawEmpty bool
	for _, m := range facets.Models {
		if m.Value == "" {
			sawEmpty = true
		}
	}
	if !sawEmpty {
		t.Errorf("models = %+v, want the empty value among them", facets.Models)
	}

	if len(facets.Clients) != 3 || len(facets.IPs) != 3 {
		t.Errorf("clients %d, ips %d; want 3 of each", len(facets.Clients), len(facets.IPs))
	}

	// 0 is not a code any upstream sent, so it is named rather than printed.
	var zero *FacetValue
	for i := range facets.Statuses {
		if facets.Statuses[i].Value == "0" {
			zero = &facets.Statuses[i]
		}
	}
	if zero == nil {
		t.Fatalf("statuses = %+v, want the no-answer row", facets.Statuses)
	}
	if zero.Label != "no answer" {
		t.Errorf("status 0 labelled %q, want %q", zero.Label, "no answer")
	}
}

// The counts are totals on purpose: a menu that answered to the filter in
// force would hide the values needed to widen it.
func TestRequestFacetsIgnoreTheFilterInForce(t *testing.T) {
	st := usageStore(t)
	now := time.Now()

	record(t, st, UsageEvent{At: now.Add(-2 * time.Minute), Model: "sonnet", Status: 200})
	record(t, st, UsageEvent{At: now.Add(-1 * time.Minute), Model: "haiku", Status: 200})

	facets, err := st.RequestFacets(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(facets.Models) != 2 {
		t.Errorf("models = %+v, want both however the table is filtered", facets.Models)
	}
}
