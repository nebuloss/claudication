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
// when it refills. Measured on a free account: token type WTUS, every reset
// a week out. The windows are per model rather than per account — a spent
// Pro quota leaves Flash serving — and their length is not said, only their
// reset; so nothing maps onto the store's five-hour and weekly columns, and
// the rows live in the detail, one per model, for the accounts screen.

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

// quotaRow is one model's quota as the detail keeps it.
type quotaRow struct {
	Model       string  `json:"model"`
	TokenType   string  `json:"token_type,omitempty"`
	UsedPercent float64 `json:"used_percent"`
	// ResetAt is unix seconds; 0 is not said.
	ResetAt int64 `json:"reset_at,omitempty"`
}

// quota is the answer as the provider-neutral quota: no windows, the rows as
// its detail, and limited only when every model is spent — while one model
// serves, the account does.
func (q quotaResponse) quota() provider.Quota {
	out := provider.Quota{At: time.Now()}
	var rows []quotaRow
	spent := 0
	for _, b := range q.Buckets {
		// Antigravity's internal models (chat_20706) are not a person's.
		if b.ModelID == "" || strings.Contains(b.ModelID, "_") {
			continue
		}
		row := quotaRow{Model: b.ModelID, TokenType: b.TokenType}
		if b.RemainingFraction != nil {
			row.UsedPercent = min(max((1-*b.RemainingFraction)*100, 0), 100)
		}
		if t, err := time.Parse(time.RFC3339, b.ResetTime); err == nil {
			row.ResetAt = t.Unix()
		}
		if row.UsedPercent >= 100 {
			spent++
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rank(rows[i].Model).before(rank(rows[j].Model)) })
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
	return q.quota(), nil
}

// limitRows reads the detail back as the accounts screen's rows.
//
// Models that draw on one allowance are one row. Measured 2026-10-23 on a
// free account: every Gemini model reported the same fraction and the same
// reset — one allowance, fifteen buckets — while Claude Opus, Claude Sonnet
// and GPT-OSS each had their own. So rows of one line (gemini, gpt) that
// agree on both are folded, "Gemini — 15 models", and a model alone keeps its
// own name. Claude's rows are left out: the gateway never sends Claude there
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
		n     int
	}
	var groups []*group
	for _, r := range rows {
		if strings.HasPrefix(r.Model, "claude-") {
			continue
		}
		line, _, _ := strings.Cut(r.Model, "-")
		var g *group
		for _, h := range groups {
			if h.line == line && h.first.UsedPercent == r.UsedPercent && h.first.ResetAt == r.ResetAt {
				g = h
				break
			}
		}
		if g == nil {
			g = &group{line: line, first: r}
			groups = append(groups, g)
		}
		g.n++
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
		if g.n > 1 {
			title = displayName(g.line) + " — " + strconv.Itoa(g.n) + " models"
		}
		l := provider.Limit{
			Title:    title,
			Kind:     g.first.Model,
			Percent:  g.first.UsedPercent,
			Severity: severity(g.first.UsedPercent),
			IsActive: i == binding,
		}
		if g.first.ResetAt > 0 {
			l.ResetsAt = time.Unix(g.first.ResetAt, 0).UTC().Format(time.RFC3339)
		}
		out = append(out, l)
	}
	return out
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
