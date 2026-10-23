package google

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"claudication/internal/provider"
)

// The account's quota, as gemini-cli reads it for /stats (server.ts
// retrieveUserQuota; types.ts RetrieveUserQuotaResponse):
//
//	POST {base}/v1internal:retrieveUserQuota   {"project": <project>}
//	{"buckets": [{"modelId", "tokenType", "remainingFraction",
//	              "remainingAmount", "resetTime"}, …]}
//
// One bucket per model and kind of token, each with the fraction left and
// when it refills. The windows are per model rather than per account — a
// spent Pro quota leaves Flash serving — and their length is not said, only
// their reset; so nothing maps onto the store's five-hour and weekly
// columns, and the rows live in the detail, one per model, for the accounts
// screen.

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
		if b.ModelID == "" {
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

// limitRows reads the detail back as the accounts screen's rows, one per
// model, titled as the model is. The binding one is the fullest, as for
// ChatGPT: the backend does not say.
func limitRows(detail string) []provider.Limit {
	var rows []quotaRow
	if json.Unmarshal([]byte(detail), &rows) != nil {
		return nil
	}
	binding := -1
	for i, r := range rows {
		if r.UsedPercent > 0 && (binding < 0 || r.UsedPercent > rows[binding].UsedPercent) {
			binding = i
		}
	}
	out := make([]provider.Limit, 0, len(rows))
	for i, r := range rows {
		l := provider.Limit{
			Title:    displayName(r.Model),
			Kind:     r.Model,
			Percent:  r.UsedPercent,
			Severity: severity(r.UsedPercent),
			IsActive: i == binding,
		}
		if r.ResetAt > 0 {
			l.ResetsAt = time.Unix(r.ResetAt, 0).UTC().Format(time.RFC3339)
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
