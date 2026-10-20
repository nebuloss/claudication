package uptime

import (
	"sort"
	"time"

	"claudication/internal/store"
)

// Report is the whole of GET /status.json. Every field is a state, a
// fraction of time or a moment; docs/status-page.md says why each is safe to
// publish, and TestReportFields holds the JSON to exactly these keys.
type Report struct {
	// State is now: up, degraded or paused. Never down — a gateway that is
	// down does not answer.
	State string `json:"state"`
	// Uptime is the share of each window the gateway was up, out of the time
	// it was not paused. Null for a window with no history in it.
	Uptime Windows `json:"uptime"`
	// Hours are the last 24 UTC hours, the current one last; Days the last 90
	// UTC days, today last.
	Hours []Bucket `json:"hours"`
	Days  []Bucket `json:"days"`
	// Incidents are the stretches of the last 90 days that were not up or
	// paused, newest first, at most 20.
	Incidents []Incident `json:"incidents"`
	// Generated is when this was worked out, unix milliseconds. The answer is
	// cached for a minute, and this is how a reader knows how old it is.
	Generated int64 `json:"generated"`
}

// Windows are the headline figures.
type Windows struct {
	Day     *float64 `json:"24h"`
	Week    *float64 `json:"7d"`
	Quarter *float64 `json:"90d"`
}

// Bucket is one bar: its start, the worst state in it, and the share of it
// that was up, out of the time it was not paused (null when that is none).
type Bucket struct {
	Start int64    `json:"start"`
	State string   `json:"state"`
	Up    *float64 `json:"up"`
}

// Incident is one stretch of a single state that was not up: when it began,
// when it ended (null while it goes on), and which it was.
type Incident struct {
	Start int64  `json:"start"`
	End   *int64 `json:"end"`
	State string `json:"state"`
}

// span is a stretch of the timeline in one state.
type span struct {
	start, end time.Time
	state      State
}

// timeline turns the samples into contiguous spans up to now: each sample
// covers its minute, a gap of at most Tolerance carries the state before it
// on, and a longer one is Down. Nothing before the first sample — that is
// None, which the buckets read as no data rather than as an outage.
//
// The gap after the last sample is read the same way, so a report built
// from samples that stopped is honest about it; a running gateway samples
// every minute, so for it the tail is always short.
func timeline(samples []store.UptimeSample, now time.Time) []span {
	var out []span
	add := func(start, end time.Time, st State) {
		if !end.After(start) {
			return
		}
		if n := len(out); n > 0 && out[n-1].state == st && !out[n-1].end.Before(start) {
			out[n-1].end = maxTime(out[n-1].end, end)
			return
		}
		out = append(out, span{start, end, st})
	}
	for i, smp := range samples {
		st := State(smp.State)
		if st < Up || st > Degraded {
			st = Degraded
		}
		start := smp.Minute
		if start.After(now) {
			break
		}
		end := start.Add(Every)
		next := now
		if i+1 < len(samples) && samples[i+1].Minute.Before(now) {
			next = samples[i+1].Minute
		}
		if next.Sub(end) <= Tolerance {
			add(start, maxTime(end, next), st)
			continue
		}
		add(start, end, st)
		add(end, next, Down)
	}
	// The last sample's minute can run past now; the timeline stops at now.
	if n := len(out); n > 0 && out[n-1].end.After(now) {
		out[n-1].end = now
	}
	return out
}

// overlay lays the recorded stops over the timeline: each is Down for exactly
// as long as it lasted, whatever the minute samples around it say. A sample
// covers its whole minute, so without this a restart inside one minute — the
// half minute a deploy waits for the last stream before the new process can
// listen — read as a minute up, and a page that is meant to be honest about
// availability said 100% over a stretch in which requests were refused.
//
// An outage with no end is cut at now: one is only open while the gateway is
// stopped, and the next start closes it.
func overlay(spans []span, outages []store.UptimeOutage, now time.Time) []span {
	for _, o := range outages {
		end := o.End
		if end.IsZero() || end.After(now) {
			end = now
		}
		if !end.After(o.Start) {
			continue
		}
		var out []span
		for _, sp := range spans {
			if !sp.end.After(o.Start) || !end.After(sp.start) {
				out = append(out, sp)
				continue
			}
			if sp.start.Before(o.Start) {
				out = append(out, span{sp.start, o.Start, sp.state})
			}
			if end.Before(sp.end) {
				out = append(out, span{end, sp.end, sp.state})
			}
		}
		out = append(out, span{o.Start, end, Down})
		spans = merged(out)
	}
	return spans
}

// merged sorts spans by start and joins neighbours in the same state.
func merged(spans []span) []span {
	sort.Slice(spans, func(i, j int) bool { return spans[i].start.Before(spans[j].start) })
	var out []span
	for _, sp := range spans {
		if n := len(out); n > 0 && out[n-1].state == sp.state && !out[n-1].end.Before(sp.start) {
			out[n-1].end = maxTime(out[n-1].end, sp.end)
			continue
		}
		out = append(out, sp)
	}
	return out
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// tally is how long a stretch spent in each state.
type tally [Down + 1]time.Duration

func (t tally) worst() State {
	for st := Down; st > None; st-- {
		if t[st] > 0 {
			return st
		}
	}
	return None
}

// up is the share of the counted time that was up; paused and None are not
// counted. Nil when nothing was.
func (t tally) up() *float64 {
	counted := t[Up] + t[Degraded] + t[Down]
	if counted <= 0 {
		return nil
	}
	f := float64(t[Up]) / float64(counted)
	return &f
}

func measure(spans []span, from, to time.Time) tally {
	var t tally
	for _, sp := range spans {
		s, e := maxTime(sp.start, from), sp.end
		if e.After(to) {
			e = to
		}
		if e.After(s) {
			t[sp.state] += e.Sub(s)
		}
	}
	return t
}

func buckets(spans []span, first time.Time, step time.Duration, n int, now time.Time) []Bucket {
	out := make([]Bucket, 0, n)
	for i := range n {
		start := first.Add(time.Duration(i) * step)
		end := start.Add(step)
		if end.After(now) {
			end = now
		}
		t := measure(spans, start, end)
		out = append(out, Bucket{Start: start.UnixMilli(), State: t.worst().String(), Up: t.up()})
	}
	return out
}

// Build is the report from the samples at now, with current the state the
// gateway is in as it answers. Pure, so every rule above is tested on it
// without a clock or a database.
func Build(samples []store.UptimeSample, outages []store.UptimeOutage, now time.Time, current State) Report {
	now = now.UTC()
	spans := overlay(timeline(samples, now), outages, now)
	r := Report{
		State:     current.String(),
		Generated: now.UnixMilli(),
		Uptime: Windows{
			Day:     measure(spans, now.Add(-24*time.Hour), now).up(),
			Week:    measure(spans, now.Add(-7*24*time.Hour), now).up(),
			Quarter: measure(spans, now.Add(-History), now).up(),
		},
		// UTC hours and days, so every reader sees the same bars whatever
		// their timezone; the page labels them as UTC.
		Hours:     buckets(spans, now.Truncate(time.Hour).Add(-23*time.Hour), time.Hour, 24, now),
		Days:      buckets(spans, now.Truncate(24*time.Hour).Add(-89*24*time.Hour), 24*time.Hour, 90, now),
		Incidents: []Incident{},
	}
	horizon := now.Add(-History)
	for i := len(spans) - 1; i >= 0 && len(r.Incidents) < maxIncidents; i-- {
		sp := spans[i]
		if !sp.end.After(horizon) {
			break
		}
		if sp.state != Degraded && sp.state != Down {
			continue
		}
		in := Incident{Start: maxTime(sp.start, horizon).UnixMilli(), State: sp.state.String()}
		if sp.end.Before(now) {
			end := sp.end.UnixMilli()
			in.End = &end
		}
		r.Incidents = append(r.Incidents, in)
	}
	return r
}
