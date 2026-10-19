package openai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"claudication/internal/provider"
)

// The usage windows, as the backend reports them on every answer.
//
// Measured 2026-10-19 on a Plus account (testdata/baseline.json), one family
// of headers per window, primary and secondary:
//
//	x-codex-primary-used-percent          0
//	x-codex-primary-window-minutes        300
//	x-codex-primary-reset-at              1792446828   unix seconds
//	x-codex-primary-reset-after-seconds   17956
//	x-codex-secondary-…                   the same, window-minutes 10080
//
// plus x-codex-plan-type, -active-limit, -credits-*, -turn-state and a few
// others this does not read. reset-after-seconds was not in Codex's source
// (codex-api/src/rate_limits.rs reads the other three) and is on the wire, so
// it is the fallback when reset-at is missing.
//
// Primary was the 5-hour window and secondary the weekly one, as everyone
// says; nothing in the source promises it, so the windows go to the store by
// their length (store.QuotaFromWindows), and a plan that reports others
// still shows them on the accounts screen from the detail.
//
// Codex also reads other families, x-<name>-primary-used-percent, by
// scanning; none was on the wire and none is read here.

// windowNames are the header families read, in the order rows are shown.
var windowNames = []string{"primary", "secondary"}

// windowRow is one window as the detail keeps it: the shape both the headers
// and /wham/usage are turned into, so the accounts screen reads one.
type windowRow struct {
	// Name is the backend's: primary, secondary, or an additional limit's.
	Name          string  `json:"name"`
	WindowSeconds int64   `json:"window_seconds"`
	UsedPercent   float64 `json:"used_percent"`
	// ResetAt is unix seconds; 0 is not said.
	ResetAt int64 `json:"reset_at,omitempty"`
}

// quotaFromHeaders reads the windows off an answer's headers. ok is false when
// the answer carried none, which is not the same as an idle account.
func quotaFromHeaders(h http.Header) (provider.Quota, bool) {
	var rows []windowRow
	for _, name := range windowNames {
		prefix := "X-Codex-" + name + "-"
		used, err := strconv.ParseFloat(strings.TrimSpace(h.Get(prefix+"Used-Percent")), 64)
		if err != nil {
			continue
		}
		minutes, err := strconv.ParseInt(strings.TrimSpace(h.Get(prefix+"Window-Minutes")), 10, 64)
		if err != nil || minutes <= 0 {
			continue
		}
		row := windowRow{Name: name, WindowSeconds: minutes * 60, UsedPercent: used}
		if at, err := strconv.ParseInt(strings.TrimSpace(h.Get(prefix+"Reset-At")), 10, 64); err == nil && at > 0 {
			row.ResetAt = at
		} else if after, err := strconv.ParseInt(strings.TrimSpace(h.Get(prefix+"Reset-After-Seconds")), 10, 64); err == nil && after >= 0 {
			row.ResetAt = time.Now().Add(time.Duration(after) * time.Second).Unix()
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return provider.Quota{}, false
	}
	q := quotaFromRows(rows)
	// The backend names the limit that bit, when one did. Codex reads it as
	// x-codex-rate-limit-reached-type; empty is none.
	if strings.TrimSpace(h.Get("X-Codex-Rate-Limit-Reached-Type")) != "" {
		q.Limited = true
	}
	return q, true
}

// quotaFromRows is the rows as the provider-neutral quota, the rows
// themselves its detail.
func quotaFromRows(rows []windowRow) provider.Quota {
	q := provider.Quota{At: time.Now()}
	for _, r := range rows {
		w := provider.Window{Seconds: r.WindowSeconds, UsedPercent: r.UsedPercent, Status: "allowed"}
		if r.ResetAt > 0 {
			w.ResetAt = time.Unix(r.ResetAt, 0).UTC()
		}
		if r.UsedPercent >= 100 {
			w.Status = "limit_reached"
		}
		q.Windows = append(q.Windows, w)
	}
	if raw, err := json.Marshal(rows); err == nil {
		q.Detail = string(raw)
	}
	return q
}

// limitRows reads the detail back as the accounts screen's rows, each titled
// from its window's length — "5-hour limit", "Weekly limit" — because the
// backend's names (primary, secondary) say nothing to an operator.
func limitRows(detail string) []provider.Limit {
	var rows []windowRow
	if json.Unmarshal([]byte(detail), &rows) != nil {
		return nil
	}
	out := make([]provider.Limit, 0, len(rows))
	for _, r := range rows {
		l := provider.Limit{
			Title:         windowTitle(r),
			Kind:          r.Name,
			Percent:       r.UsedPercent,
			Severity:      severity(r.UsedPercent),
			IsActive:      true,
			WindowSeconds: r.WindowSeconds,
		}
		if r.ResetAt > 0 {
			l.ResetsAt = time.Unix(r.ResetAt, 0).UTC().Format(time.RFC3339)
		}
		out = append(out, l)
	}
	return out
}

// windowTitle names a window by its length, the backend's name kept for one
// that is neither of the two everyone has.
func windowTitle(r windowRow) string {
	label := WindowLabel(r.WindowSeconds)
	if r.Name != "primary" && r.Name != "secondary" && r.Name != "" {
		return r.Name + " (" + label + ")"
	}
	return label
}

// WindowLabel is a window's length as the accounts screen says it.
func WindowLabel(seconds int64) string {
	switch d := time.Duration(seconds) * time.Second; {
	case d <= 0:
		return "Limit"
	case d == 7*24*time.Hour:
		return "Weekly limit"
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%d-day limit", d/(24*time.Hour))
	case d%time.Hour == 0:
		return fmt.Sprintf("%d-hour limit", d/time.Hour)
	default:
		return fmt.Sprintf("%d-minute limit", d/time.Minute)
	}
}

// severity is the accounts screen's tone for a window, in the words
// Anthropic's limits use, so one stylesheet reads both.
func severity(used float64) string {
	switch {
	case used >= 100:
		return "rejected"
	case used >= 80:
		return "warning"
	}
	return "normal"
}

// apiError is the backend's error body, in every shape measured or read:
//
//	{"error":{"type":"usage_limit_reached","resets_at":1792446828,...}}   429
//	{"error":{"code":"context_length_exceeded","message":"..."}}          stream
//	{"detail":"Bad Request"}                                               400
//
// The first two from Codex's source (codex-api/src/api_bridge.rs,
// sse/responses_error.rs); the third measured, for a lone surrogate.
type apiError struct {
	Error struct {
		Type     string          `json:"type"`
		Code     string          `json:"code"`
		Message  string          `json:"message"`
		ResetsAt json.RawMessage `json:"resets_at"`
		PlanType string          `json:"plan_type"`
	} `json:"error"`
	Detail json.RawMessage `json:"detail"`
}

func parseError(body []byte) apiError {
	var e apiError
	_ = json.Unmarshal(body, &e)
	return e
}

// kind is the error's own word for itself: its type when that says more than
// "error", else its code.
func (e apiError) kind() string {
	if t := e.Error.Type; t != "" && t != "error" {
		return t
	}
	return e.Error.Code
}

// resetsAt is the usage limit's reset. Codex reads it as unix seconds; a
// number in a string is taken as well, since nothing else would be meant.
func (e apiError) resetsAt() time.Time {
	raw := strings.Trim(string(e.Error.ResetsAt), `"`)
	if raw == "" || raw == "null" {
		return time.Time{}
	}
	secs, err := strconv.ParseFloat(raw, 64)
	if err != nil || secs <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(secs), 0).UTC()
}
