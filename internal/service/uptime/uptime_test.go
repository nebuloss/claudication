package uptime

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"sort"
	"testing"
	"time"

	"claudication/internal/store"
)

// now is a fixed moment half a minute into a minute, in the middle of a UTC
// day, so the hourly and daily buckets have something on both sides.
var now = time.Date(2026, 10, 20, 12, 0, 30, 0, time.UTC)

// minutes is one sample a minute in state st, from from for n minutes.
func minutes(from time.Time, n int, st State) []store.UptimeSample {
	out := make([]store.UptimeSample, n)
	for i := range out {
		out[i] = store.UptimeSample{Minute: from.Truncate(time.Minute).Add(time.Duration(i) * time.Minute), State: int(st)}
	}
	return out
}

// steady is a gateway up every minute for the last d, the current minute
// included.
func steady(d time.Duration) []store.UptimeSample {
	n := int(d / time.Minute)
	return minutes(now.Truncate(time.Minute).Add(-time.Duration(n-1)*time.Minute), n, Up)
}

// without removes the samples in [from, to): the process was not running.
func without(s []store.UptimeSample, from, to time.Time) []store.UptimeSample {
	var out []store.UptimeSample
	for _, x := range s {
		if x.Minute.Before(from) || !x.Minute.Before(to) {
			out = append(out, x)
		}
	}
	return out
}

func val(f *float64) float64 {
	if f == nil {
		return -1
	}
	return *f
}

// A deploy restarts the process in seconds and misses a minute or two of
// samples. That is the gateway running, not an outage: no incident, every
// bar up, and the figures at 100%.
func TestADeployRestartIsNotAnOutage(t *testing.T) {
	s := without(steady(48*time.Hour), now.Add(-3*time.Hour), now.Add(-3*time.Hour+2*time.Minute))
	r := Build(s, now, Up)
	if len(r.Incidents) != 0 {
		t.Errorf("incidents = %+v, want none", r.Incidents)
	}
	if val(r.Uptime.Day) != 1 || val(r.Uptime.Week) != 1 {
		t.Errorf("uptime = %v %v, want 1", val(r.Uptime.Day), val(r.Uptime.Week))
	}
	for _, b := range r.Hours {
		if b.State != "up" {
			t.Errorf("hour %d is %s", b.Start, b.State)
		}
	}
}

// A gap longer than the tolerance is the process not running: down, from the
// end of the last sample's minute to the next sample, and an incident with
// both ends.
func TestALongGapIsDown(t *testing.T) {
	from, to := now.Add(-3*time.Hour).Truncate(time.Minute), now.Add(-3*time.Hour+30*time.Minute).Truncate(time.Minute)
	r := Build(without(steady(48*time.Hour), from, to), now, Up)
	if len(r.Incidents) != 1 {
		t.Fatalf("incidents = %+v, want one", r.Incidents)
	}
	in := r.Incidents[0]
	if in.State != "down" || in.Start != from.UnixMilli() || in.End == nil || *in.End != to.UnixMilli() {
		t.Errorf("incident = %+v, want down %d–%d", in, from.UnixMilli(), to.UnixMilli())
	}
	if want := 1 - 30.0/(24*60); val(r.Uptime.Day) < want-1e-9 || val(r.Uptime.Day) > want+1e-9 {
		t.Errorf("24h uptime = %v, want %v", val(r.Uptime.Day), want)
	}
	hour := r.Hours[len(r.Hours)-4] // 09:00–10:00, which the outage falls in
	if hour.State != "down" || val(hour.Up) != 0.5 {
		t.Errorf("hour of the outage = %+v, want down at 0.5", hour)
	}
}

// Paused is the operator's choice: drawn as its own state, never an incident,
// and left out of the uptime figures rather than counted against them.
func TestPausedIsNeitherAnIncidentNorDowntime(t *testing.T) {
	s := steady(48 * time.Hour)
	hour := now.Truncate(time.Hour)
	for i := range s {
		if m := s[i].Minute; !m.Before(hour.Add(-5*time.Hour)) && m.Before(hour.Add(-4*time.Hour)) {
			s[i].State = int(Paused)
		}
	}
	r := Build(s, now, Up)
	if len(r.Incidents) != 0 || val(r.Uptime.Day) != 1 {
		t.Errorf("incidents %+v, uptime %v; want none and 1", r.Incidents, val(r.Uptime.Day))
	}
	if got := r.Hours[len(r.Hours)-6]; got.State != "paused" || got.Up != nil {
		t.Errorf("the paused hour = %+v, want paused with no up share", got)
	}
}

// Degraded until now is an incident still going on: no end.
func TestAnOngoingIncidentHasNoEnd(t *testing.T) {
	s := steady(2 * time.Hour)
	for i := len(s) - 10; i < len(s); i++ {
		s[i].State = int(Degraded)
	}
	r := Build(s, now, Degraded)
	if r.State != "degraded" || len(r.Incidents) != 1 || r.Incidents[0].End != nil || r.Incidents[0].State != "degraded" {
		t.Errorf("state %s, incidents %+v; want one open degraded incident", r.State, r.Incidents)
	}
}

// Before the first sample is no data, not an outage: a gateway installed an
// hour ago has 89 empty days and a full hour, not 89 red days.
func TestBeforeTheHistoryIsNoData(t *testing.T) {
	r := Build(steady(time.Hour), now, Up)
	if len(r.Days) != 90 || len(r.Hours) != 24 {
		t.Fatalf("%d days, %d hours", len(r.Days), len(r.Hours))
	}
	for _, b := range r.Days[:89] {
		if b.State != "none" || b.Up != nil {
			t.Fatalf("a day before the history = %+v", b)
		}
	}
	if today := r.Days[89]; today.State != "up" || val(today.Up) != 1 {
		t.Errorf("today = %+v", today)
	}
	if val(r.Uptime.Quarter) != 1 || len(r.Incidents) != 0 {
		t.Errorf("90d %v, incidents %+v", val(r.Uptime.Quarter), r.Incidents)
	}
	if empty := Build(nil, now, Up); empty.Uptime.Day != nil || len(empty.Incidents) != 0 {
		t.Errorf("no samples at all: %+v", empty.Uptime)
	}
}

// Samples that stopped long ago are down since, even though whoever asks is
// the running process: the report reads the record, and the record has a gap.
func TestTheTailAfterTheLastSampleIsReadLikeAGap(t *testing.T) {
	s := minutes(now.Add(-2*time.Hour), 60, Up)
	r := Build(s, now, Up)
	if len(r.Incidents) != 1 || r.Incidents[0].State != "down" || r.Incidents[0].End != nil {
		t.Errorf("incidents = %+v, want one open down", r.Incidents)
	}
}

// The JSON is exactly these keys, at every level. A field added to Report is
// published to anyone who asks, so adding one has to change this test, and
// whoever changes it has to say in docs/status-page.md why it is safe.
func TestReportFields(t *testing.T) {
	s := without(steady(48*time.Hour), now.Add(-3*time.Hour), now.Add(-2*time.Hour))
	raw, err := json.Marshal(Build(s, now, Up))
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	keysOf := func(raw json.RawMessage) []string {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		var k []string
		for key := range m {
			k = append(k, key)
		}
		sort.Strings(k)
		return k
	}
	first := func(raw json.RawMessage) json.RawMessage {
		var a []json.RawMessage
		if err := json.Unmarshal(raw, &a); err != nil || len(a) == 0 {
			t.Fatalf("%s: want a non-empty array", raw)
		}
		return a[0]
	}
	for name, c := range map[string]struct {
		raw  json.RawMessage
		want []string
	}{
		"report":   {raw, []string{"days", "generated", "hours", "incidents", "state", "uptime"}},
		"uptime":   {top["uptime"], []string{"24h", "7d", "90d"}},
		"hour":     {first(top["hours"]), []string{"start", "state", "up"}},
		"day":      {first(top["days"]), []string{"start", "state", "up"}},
		"incident": {first(top["incidents"]), []string{"end", "start", "state"}},
	} {
		if got := keysOf(c.raw); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s keys = %v, want %v", name, got, c.want)
		}
	}
}

// memStore is the Store in memory, with the store's worst-in-the-minute rule.
type memStore struct {
	rows map[int64]int
}

func (m *memStore) RecordUptime(_ context.Context, at time.Time, state int) error {
	k := at.Truncate(time.Minute).UnixMilli()
	m.rows[k] = max(m.rows[k], state)
	return nil
}

func (m *memStore) UptimeSince(_ context.Context, from time.Time) ([]store.UptimeSample, error) {
	var out []store.UptimeSample
	for k, v := range m.rows {
		if k >= from.UnixMilli() {
			out = append(out, store.UptimeSample{Minute: time.UnixMilli(k).UTC(), State: v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Minute.Before(out[j].Minute) })
	return out, nil
}

func (m *memStore) PruneUptime(_ context.Context, before time.Time) (int64, error) {
	var n int64
	for k := range m.rows {
		if k < before.UnixMilli() {
			delete(m.rows, k)
			n++
		}
	}
	return n, nil
}

// The sampler records what the probe says, the worse of two in a minute, a
// state it does not know as degraded; it prunes at the longer of the usage
// retention and the history; and it answers a cached report for a minute.
func TestSampler(t *testing.T) {
	st := &memStore{rows: map[int64]int{}}
	clock := now
	state := Up
	s := &Sampler{
		Store: st, Probe: func(context.Context) State { return state },
		Retention: 30 * 24 * time.Hour, Log: slog.New(slog.DiscardHandler),
		Now: func() time.Time { return clock },
	}
	ctx := context.Background()
	mustSample := func() {
		t.Helper()
		if err := s.Sample(ctx); err != nil {
			t.Fatal(err)
		}
	}
	mustSample()
	state = Degraded
	clock = now.Add(10 * time.Second)
	mustSample()
	state = Up
	clock = now.Add(20 * time.Second)
	mustSample()
	if got := st.rows[now.Truncate(time.Minute).UnixMilli()]; got != int(Degraded) {
		t.Errorf("minute = %d, want the degraded sample to stand", got)
	}
	state = State(42)
	clock = now.Add(time.Minute)
	mustSample()
	if got := st.rows[now.Add(time.Minute).Truncate(time.Minute).UnixMilli()]; got != int(Degraded) {
		t.Errorf("an unknown state was recorded as %d", got)
	}

	// Pruning keeps 90 days although retention is 30.
	old, kept := now.Add(-100*24*time.Hour), now.Add(-60*24*time.Hour)
	st.rows[old.UnixMilli()], st.rows[kept.UnixMilli()] = 1, 1
	if n, _ := s.Prune(ctx); n != 1 {
		t.Errorf("pruned %d, want the 100-day-old sample only", n)
	}
	if _, ok := st.rows[kept.UnixMilli()]; !ok {
		t.Error("a 60-day-old sample was pruned under a 30-day retention")
	}

	state = Up
	r1, _ := s.Report(ctx)
	state = Paused
	clock = clock.Add(30 * time.Second)
	if r2, _ := s.Report(ctx); r2.State != r1.State || r2.Generated != r1.Generated {
		t.Errorf("a report within the minute was rebuilt: %s then %s", r1.State, r2.State)
	}
	clock = clock.Add(time.Minute)
	if r3, _ := s.Report(ctx); r3.State != "paused" {
		t.Errorf("after a minute the report says %s, want paused", r3.State)
	}
}
