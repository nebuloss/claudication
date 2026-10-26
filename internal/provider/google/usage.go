package google

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"claudication/internal/provider"
)

// The account's quota (measured 2026-10-23, testdata/antigravity-quota.json;
// gemini-cli reads the same answer for /stats, server.ts retrieveUserQuota):
//
//	POST {base}/v1internal:retrieveUserQuota   {"project": <project>}
//	{"buckets": [{"modelId", "tokenType", "remainingFraction",
//	              "remainingAmount", "resetTime"}, …]}
//
// One bucket per model and kind of token, each with the fraction left and
// when it refills — nothing else: no amount, no window length.
//
// How it behaves, measured 2026-10-23 on a free account (three turns of
// gemini-3.8-flash-high, two of gpt-oss-120b-medium, a snapshot around
// each):
//
//   - One allowance per line of models, not per model: every Gemini bucket
//     moved together, by the same amount, and GPT-OSS alone moved for its
//     turns. Claude Opus and Sonnet have one each.
//   - Counted in tokens, thinking included: Gemini fell 2.835% for 18,963
//     tokens, about 670,000 a week; GPT-OSS 0.111% for 6,335, about
//     5.7 million.
//   - A window is a week from its line's first use: Gemini's reset sat at
//     the first turn's time plus seven days, and GPT-OSS's settled on its
//     first turn's. An allowance never used has no window yet, and reports
//     a reset a week from whenever it is asked.
//
// Measured again 2026-10-23 on the same account an hour after it moved to
// Google AI Pro: the windows are not all a week. Every model but one line
// reported a reset five hours out, gemini-4-argon's three a week out, all
// unused — so an allowance's length is the plan's, and per line. Nothing
// says it but the reset of an allowance not yet used, which is a window
// from now; so windowOf reads it off the reset, as the shortest known
// length that holds it.
//
// The windows are per line rather than per account — a spent Gemini
// allowance leaves GPT-OSS serving — so nothing maps onto the store's
// five-hour and weekly columns, which the pool reads for the whole account;
// the rows live in the detail, for the accounts screen.

// bucket is BucketInfo (types.ts).
type bucket struct {
	ModelID           string   `json:"modelId"`
	TokenType         string   `json:"tokenType"`
	RemainingFraction *float64 `json:"remainingFraction"`
	RemainingAmount   string   `json:"remainingAmount"`
	ResetTime         string   `json:"resetTime"`
}

// quotaResponse is RetrieveUserQuotaResponse.
type quotaResponse struct {
	Buckets []bucket `json:"buckets"`
}

// windows are the allowance lengths seen, shortest first (measured; see
// above): five hours on Google AI Pro, a week on the free tier and for
// Gemini 4 Argon on Pro.
var windows = []int64{5 * 60 * 60, 7 * 24 * 60 * 60}

// windowOf is the length of a window that ends in left: the shortest known
// one that holds it, give or take a minute for the read. A used window's
// reset is nearer than its length, so a week with under five hours left
// reads as five hours: the bound of reading lengths off resets.
func windowOf(left time.Duration) int64 {
	for _, w := range windows {
		if left <= time.Duration(w)*time.Second+time.Minute {
			return w
		}
	}
	return windows[len(windows)-1]
}

// quotaRow is one model's quota as the detail keeps it.
type quotaRow struct {
	Model       string  `json:"model"`
	TokenType   string  `json:"token_type,omitempty"`
	UsedPercent float64 `json:"used_percent"`
	// ResetAt is unix seconds; 0 is not said.
	ResetAt int64 `json:"reset_at,omitempty"`
	// Window is the allowance's length in seconds (windowOf); 0 is not said.
	Window int64 `json:"window,omitempty"`
}

// quota is the answer, read at now, as the provider-neutral quota: no windows, the rows as
// its detail, and limited only when every model is spent — while one model
// serves, the account does. Claude's buckets are left out: the gateway
// never sends Claude here (ModelList), so an untouched Claude allowance
// would keep a spent account looking open.
func (q quotaResponse) quota(now time.Time) provider.Quota {
	out := provider.Quota{At: now}
	var rows []quotaRow
	spent := 0
	for _, b := range q.Buckets {
		// Antigravity's internal models (chat_20706) are not a person's.
		if b.ModelID == "" || strings.Contains(b.ModelID, "_") || strings.HasPrefix(b.ModelID, "claude-") {
			continue
		}
		row := quotaRow{Model: b.ModelID, TokenType: b.TokenType}
		if b.RemainingFraction != nil {
			row.UsedPercent = min(max((1-*b.RemainingFraction)*100, 0), 100)
		}
		// An allowance not yet used has no window running: its reset is a
		// window from the moment it was asked, and moves with every read.
		if t, err := time.Parse(time.RFC3339, b.ResetTime); err == nil {
			// A reset already past — a stale answer, a skewed clock — says
			// nothing of the length.
			if left := t.Sub(now); left > 0 {
				row.Window = windowOf(left)
			}
			if row.UsedPercent > 0 {
				row.ResetAt = t.Unix()
			}
		}
		if row.UsedPercent >= 100 {
			spent++
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rank(rows[i].Model, nil).before(rank(rows[j].Model, nil)) })
	out.Limited = len(rows) > 0 && spent == len(rows)
	if raw, err := json.Marshal(rows); err == nil {
		out.Detail = string(raw)
	}
	return out
}

// FetchUsage asks what an account has left. The token is already fresh: the
// pool refreshed it.
func FetchUsage(ctx context.Context, client *http.Client, cred provider.Credential) (provider.Quota, error) {
	if cred.AccountID == "" {
		return provider.Quota{}, fmt.Errorf("the account has no Code Assist project; sign it in again")
	}
	var q quotaResponse
	if err := call(ctx, client, cred.Token, http.MethodPost, ":retrieveUserQuota", map[string]string{"project": cred.AccountID}, &q); err != nil {
		return provider.Quota{}, fmt.Errorf("fetch usage: %w", err)
	}
	out := q.quota(time.Now())
	// The plan, read again: it follows the account's Google One
	// subscription, not its sign-in (an account signed in free reported
	// paidTier "Google AI Pro" after an upgrade, measured 2026-10-23). A
	// failure costs only the plan's name, so it is not the usage's.
	var load loadResponse
	if call(ctx, client, cred.Token, http.MethodPost, ":loadCodeAssist", map[string]any{"metadata": clientMetadata}, &load) == nil {
		out.Plan = tierName(load.PaidTier, load.CurrentTier)
	}
	return out, nil
}

// limitRows reads the detail back as the accounts screen's rows. Claude's
// rows, which details stored before quota() left them out still carry, are
// skipped below.
//
// Models that draw on one allowance are one row. Measured 2026-10-23 on a
// free account: every Gemini model reported the same fraction and the same
// reset — one allowance, fifteen buckets — while Claude Opus, Claude Sonnet
// and GPT-OSS each had their own; on Google AI Pro, Gemini 4 Argon had a
// week's while the other Gemini models had five hours'. So rows of one line
// (gemini, gpt) that agree on fraction, reset and length are folded, named
// by what their ids share — "Gemini — 15 models", "Gemini 4 Argon — 3
// models" — and a model alone keeps its own name. Claude's rows are left out: the gateway never sends Claude there
// (ModelList), so their figure is nothing it spends. The binding row is the
// fullest, as for ChatGPT: the backend does not say.
func limitRows(detail string) []provider.Limit {
	var rows []quotaRow
	if json.Unmarshal([]byte(detail), &rows) != nil {
		return nil
	}
	type group struct {
		line  string
		first quotaRow
		ids   []string
	}
	var groups []*group
	for _, r := range rows {
		if strings.HasPrefix(r.Model, "claude-") {
			continue
		}
		line, _, _ := strings.Cut(r.Model, "-")
		var g *group
		for _, h := range groups {
			if h.line == line && h.first.UsedPercent == r.UsedPercent && h.first.ResetAt == r.ResetAt && h.first.Window == r.Window {
				g = h
				break
			}
		}
		if g == nil {
			g = &group{line: line, first: r}
			groups = append(groups, g)
		}
		g.ids = append(g.ids, r.Model)
	}
	binding := -1
	for i, g := range groups {
		if g.first.UsedPercent > 0 && (binding < 0 || g.first.UsedPercent > groups[binding].first.UsedPercent) {
			binding = i
		}
	}
	out := make([]provider.Limit, 0, len(groups))
	for i, g := range groups {
		title := displayName(g.first.Model)
		if len(g.ids) > 1 {
			title = displayName(sharedPrefix(g.ids)) + " — " + strconv.Itoa(len(g.ids)) + " models"
		}
		length := g.first.Window
		if length == 0 {
			// A row kept before lengths were: the free tier's week.
			length = windows[len(windows)-1]
		}
		l := provider.Limit{
			Title:         title,
			Kind:          g.first.Model,
			Percent:       g.first.UsedPercent,
			Severity:      severity(g.first.UsedPercent),
			IsActive:      i == binding,
			WindowSeconds: length,
		}
		if g.first.ResetAt > 0 {
			l.ResetsAt = time.Unix(g.first.ResetAt, 0).UTC().Format(time.RFC3339)
		}
		out = append(out, l)
	}
	return out
}

// sharedPrefix is the words every id starts with: gemini-4-argon for
// gemini-4-argon-high and -low, gemini for a line's several models.
func sharedPrefix(ids []string) string {
	words := strings.Split(ids[0], "-")
	for _, id := range ids[1:] {
		other := strings.Split(id, "-")
		n := 0
		for n < len(words) && n < len(other) && words[n] == other[n] {
			n++
		}
		words = words[:n]
	}
	return strings.Join(words, "-")
}

// severity is the accounts screen's tone for a row, in the words Anthropic's
// limits use, so one stylesheet reads every provider's.
func severity(used float64) string {
	switch {
	case used >= 100:
		return "rejected"
	case used >= 80:
		return "warning"
	}
	return "normal"
}
