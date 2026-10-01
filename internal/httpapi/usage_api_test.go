package httpapi

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"claudication/internal/store"
)

// refusalText is the billing message the subscription backend answers some
// requests with on their content; the log has to label it.
const refusalText = "Third-party apps now draw from your extra usage, not your plan limits."

// seedHistory3 records three requests: one that worked, one refused upstream
// on its content, and one the gateway refused itself. Newest first in that
// order.
func seedHistory3(t *testing.T, st *store.Store) {
	t.Helper()
	now := time.Now()
	for _, e := range []store.UsageEvent{
		{At: now.Add(-time.Minute), KeyID: "k1", KeyName: "laptop", AccountEmail: "a@example.com",
			Model: "claude-opus-5", Path: "/v1/messages", ConversationID: "chat-1", Client: "claude-cli",
			Status: 200, Streaming: true, InputTokens: 10, OutputTokens: 5, CacheReadTokens: 2, CacheWriteTokens: 1,
			Duration: 1500 * time.Millisecond, FirstToken: 300 * time.Millisecond, IP: "10.0.0.1"},
		{At: now.Add(-2 * time.Minute), KeyID: "k1", KeyName: "laptop", AccountEmail: "a@example.com",
			Model: "claude-sonnet-5", Path: "/v1/messages", ConversationID: "chat-1", Client: "claude-cli",
			Status: 400, Error: refusalText, ErrorCode: "content_check", IP: "10.0.0.1"},
		{At: now.Add(-3 * time.Minute), Path: "/v1/messages", Status: 401, Rejected: true,
			Error: "missing API key", IP: "10.0.0.9", Client: "curl"},
	} {
		if err := st.RecordUsage(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
}

type requestsPage struct {
	Enabled  bool          `json:"enabled"`
	Requests []requestJSON `json:"requests"`
	Next     string        `json:"next_cursor"`
}

// The request log is where a refusal gets diagnosed, so it pages without gaps,
// carries the labels that explain a refusal, and narrows by the same query
// string a shared link carries.
func TestRequestLogPagesAndFilters(t *testing.T) {
	srv, st, _ := newTestServer(t)
	seedHistory3(t, st)
	base, c := adminAPI(t, srv)

	page := func(query string) requestsPage {
		t.Helper()
		status, body := call(t, base, http.MethodGet, "/admin/requests"+query, "", c)
		if status != http.StatusOK {
			t.Fatalf("%s: %d %s", query, status, body)
		}
		return decode[requestsPage](t, body)
	}

	first := page("?limit=2")
	if !first.Enabled || len(first.Requests) != 2 || first.Next == "" {
		t.Fatalf("first page = %+v", first)
	}
	ok, refused := first.Requests[0], first.Requests[1]
	if ok.Model != "claude-opus-5" || ok.CacheTokens != 3 || ok.DurationMS != 1500 || ok.FirstTokenMS != 300 ||
		ok.Conversation != "chat-1" || ok.Client != "claude-cli" || !ok.Streaming {
		t.Errorf("a served request reads %+v", ok)
	}
	if refused.ErrorCode != "content_check" || refused.ErrorKind == "" {
		t.Errorf("the content refusal is not labelled: %+v", refused)
	}

	rest := page("?limit=2&after=" + first.Next)
	if len(rest.Requests) != 1 || !rest.Requests[0].Rejected || rest.Next != "" {
		t.Errorf("second page = %+v", rest)
	}
	// A cursor nobody can read starts from the top rather than failing.
	if got := page("?after=garbage&limit=0"); len(got.Requests) != 3 {
		t.Errorf("unreadable cursor: %d rows", len(got.Requests))
	}

	both := "?model=claude-opus-5&model=claude-sonnet-5&model=claude-opus-5"
	for query, want := range map[string]int{
		"?model=claude-opus-5":    1,
		both:                      2,
		"?chat=chat-1":            2,
		"?ip=10.0.0.9":            1,
		"?code=401&code=nonsense": 1,
		"?code=nonsense":          3,
		"?kind=rejected":          1,
		"?kind=relayed":           2,
		"?status=failed":          2,
		"?status=ok":              1,
		"?status=whatever":        3,
		"?message=content_check":  1,
		"?client=curl":            1,
	} {
		if got := page(query); len(got.Requests) != want {
			t.Errorf("%s: %d rows, want %d", query, len(got.Requests), want)
		}
	}

	status, body := call(t, base, http.MethodGet, "/admin/requests/facets", "", c)
	if status != http.StatusOK {
		t.Fatalf("facets: %d %s", status, body)
	}
	facets := decode[struct {
		Enabled bool                `json:"enabled"`
		Facets  store.RequestFacets `json:"facets"`
	}](t, body)
	if !facets.Enabled || len(facets.Facets.Models) < 2 || len(facets.Facets.IPs) != 2 {
		t.Errorf("facets = %+v", facets)
	}
}

// The usage report's window is the caller's choice, but never longer than
// what is kept: a longer one would chart an empty stretch as "no traffic".
func TestUsageReportWindow(t *testing.T) {
	srv, st, cfg := newTestServer(t)
	seedHistory3(t, st)
	base, c := adminAPI(t, srv)

	type report struct {
		Enabled       bool              `json:"enabled"`
		Days          int               `json:"days"`
		RetentionDays int               `json:"retention_days"`
		Report        store.UsageReport `json:"report"`
	}
	for query, wantDays := range map[string]int{
		"":           int(cfg.Usage.Window().Hours() / 24),
		"?days=1":    1,
		"?days=9999": cfg.Usage.RetentionDays,
		"?days=-3":   int(cfg.Usage.Window().Hours() / 24),
	} {
		status, body := call(t, base, http.MethodGet, "/admin/usage"+query, "", c)
		if status != http.StatusOK {
			t.Fatalf("%q: %d %s", query, status, body)
		}
		got := decode[report](t, body)
		if !got.Enabled || got.Days != wantDays || got.RetentionDays != cfg.Usage.RetentionDays {
			t.Errorf("%q: days = %d, want %d", query, got.Days, wantDays)
		}
		// The gateway's own refusals stay out of the aggregates.
		if got.Report.Totals.Requests != 2 {
			t.Errorf("%q: totals.requests = %d, want 2", query, got.Report.Totals.Requests)
		}
	}
}

// The first screen after sign-in answers "is this working" in one request,
// and readiness is the pool's answer, not a count of rows.
func TestOverview(t *testing.T) {
	srv, st, _ := newTestServer(t)
	seedHistory3(t, st)
	if err := seedAccount(t, st, srv); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateKey(context.Background(), "k", store.KeyLimits{}); err != nil {
		t.Fatal(err)
	}
	base, c := adminAPI(t, srv)

	status, body := call(t, base, http.MethodGet, "/admin/overview", "", c)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	got := decode[struct {
		Ready        bool               `json:"ready"`
		UsageEnabled bool               `json:"usage_enabled"`
		AdminSplit   bool               `json:"admin_split"`
		Accounts     map[string]int     `json:"accounts"`
		Keys         map[string]int     `json:"keys"`
		Last24h      *store.UsageTotals `json:"last_24h"`
		Version      string             `json:"version"`
	}](t, body)
	if !got.Ready || got.Accounts["total"] != 1 || got.Accounts["usable"] != 1 || got.Keys["total"] != 1 {
		t.Errorf("overview = %+v", got)
	}
	if !got.UsageEnabled || got.Last24h == nil || got.Last24h.Requests != 2 || got.AdminSplit || got.Version == "" {
		t.Errorf("overview = %+v", got)
	}

	// Pausing the only account makes it not ready, though it still exists.
	accounts, _ := st.ListAccounts(context.Background())
	if err := st.SetAccountDisabled(context.Background(), accounts[0].ID, true); err != nil {
		t.Fatal(err)
	}
	_, body = call(t, base, http.MethodGet, "/admin/overview", "", c)
	if decode[map[string]any](t, body)["ready"] != false {
		t.Error("a gateway whose only account is paused reports ready")
	}
}

// The chat list rolls requests up per conversation; drilling into one returns
// the same rows the request log shows, and a stale id is a 404, not an empty
// chat.
func TestChats(t *testing.T) {
	srv, st, _ := newTestServer(t)
	seedHistory3(t, st)
	base, c := adminAPI(t, srv)

	status, body := call(t, base, http.MethodGet, "/admin/chats?days=1&limit=10", "", c)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	list := decode[struct {
		Enabled bool             `json:"enabled"`
		Days    int              `json:"days"`
		Report  store.ChatReport `json:"report"`
	}](t, body)
	if !list.Enabled || list.Days != 1 || len(list.Report.Chats) != 1 || list.Report.Chats[0].ID != "chat-1" ||
		list.Report.Chats[0].Requests != 2 {
		t.Errorf("chats = %+v", list)
	}
	if status, _ := call(t, base, http.MethodGet, "/admin/chats?days=999&limit=x", "", c); status != http.StatusOK {
		t.Errorf("an out-of-range window: %d", status)
	}

	status, body = call(t, base, http.MethodGet, "/admin/chats/chat-1", "", c)
	if status != http.StatusOK {
		t.Fatalf("chat: %d %s", status, body)
	}
	one := decode[struct {
		ID       string        `json:"id"`
		Requests []requestJSON `json:"requests"`
	}](t, body)
	if one.ID != "chat-1" || len(one.Requests) != 2 || one.Requests[0].Conversation != "chat-1" {
		t.Errorf("chat = %+v", one)
	}
	if status, body := call(t, base, http.MethodGet, "/admin/chats/never-was", "", c); status != http.StatusNotFound {
		t.Errorf("unknown chat: %d %s", status, body)
	}
}

// The download is the screen's filter applied to everything kept: same query
// string, same rows, as CSV a spreadsheet opens.
func TestExportMatchesTheFilter(t *testing.T) {
	srv, st, _ := newTestServer(t)
	seedHistory3(t, st)
	base, c := adminAPI(t, srv)

	req, _ := http.NewRequest(http.MethodGet, base+"/admin/requests/export?kind=relayed", nil)
	req.AddCookie(c)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") {
		t.Fatalf("export: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `attachment; filename="requests-`) {
		t.Errorf("Content-Disposition = %q", cd)
	}
	rows, err := csv.NewReader(resp.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0][0] != "at" || len(rows[0]) != 18 {
		t.Fatalf("rows = %v", rows)
	}
	if rows[1][4] != "claude-opus-5" || rows[1][2] != "yes" || rows[1][14] != "3" || rows[1][15] != "1500" {
		t.Errorf("first row = %v", rows[1])
	}
	if rows[2][3] != "content_check" || rows[2][17] != refusalText {
		t.Errorf("refused row = %v", rows[2])
	}
}

// An export longer than one page must be complete: the rows a page boundary
// falls between are the ones a cursor bug loses.
func TestExportCrossesPages(t *testing.T) {
	srv, st, _ := newTestServer(t)
	now := time.Now()
	const n = exportPage + 37
	for i := 0; i < n; i++ {
		if err := st.RecordUsage(context.Background(), store.UsageEvent{
			At: now.Add(-time.Duration(i) * time.Second), Path: "/v1/messages", Status: 200,
			Model: fmt.Sprintf("m-%d", i), Rejected: i%2 == 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	base, c := adminAPI(t, srv)
	req, _ := http.NewRequest(http.MethodGet, base+"/admin/requests/export", nil)
	req.AddCookie(c)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rows, err := csv.NewReader(resp.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != n+1 {
		t.Fatalf("exported %d rows, want %d", len(rows)-1, n)
	}
	seen := map[string]bool{}
	for _, r := range rows[1:] {
		if seen[r[4]] {
			t.Errorf("row %s exported twice", r[4])
		}
		seen[r[4]] = true
	}
	if rows[2][2] != "no" {
		t.Errorf("a refused request is marked forwarded: %v", rows[2])
	}
}

// With usage recording off there is nothing to report, and the screens have to
// say so rather than show empty tables that read as "nobody used this".
func TestUsageScreensWhenRecordingIsOff(t *testing.T) {
	srv, _, _ := newTestServer(t)
	srv.cfg.Usage.RetentionDays = 0
	base, c := adminAPI(t, srv)

	for _, path := range []string{"/admin/usage", "/admin/requests", "/admin/requests/facets", "/admin/chats", "/admin/chats/x"} {
		status, body := call(t, base, http.MethodGet, path, "", c)
		if status != http.StatusOK || decode[map[string]any](t, body)["enabled"] != false {
			t.Errorf("%s: %d %s", path, status, body)
		}
	}
	if status, _ := call(t, base, http.MethodGet, "/admin/requests/export", "", c); status != http.StatusNotFound {
		t.Errorf("export: %d, want 404", status)
	}
	_, body := call(t, base, http.MethodGet, "/admin/overview", "", c)
	got := decode[map[string]any](t, body)
	if got["usage_enabled"] != false {
		t.Errorf("overview usage_enabled = %v", got["usage_enabled"])
	}
	if _, ok := got["last_24h"]; ok {
		t.Error("overview reports traffic figures while recording is off")
	}
}

// Filter parsing: repeated values dedupe, an empty value is a real filter for
// "nothing in this column", and an unreadable status code is dropped rather
// than failing the page.
func TestFilterParsing(t *testing.T) {
	if cleanSet(nil) != nil {
		t.Error("absent parameter narrowed")
	}
	if got := cleanSet([]string{"a", "a", ""}); len(got) != 2 || got[1] != "" {
		t.Errorf("cleanSet = %q", got)
	}
	if got := codeSet([]string{"x", "y"}); got != nil {
		t.Errorf("codeSet of nonsense = %v, want no narrowing", got)
	}
	if got := codeSet([]string{"500", "500", "429"}); len(got) != 2 {
		t.Errorf("codeSet = %v", got)
	}
	for in, want := range map[string]string{"failed": "failed", "ok": "ok", "OK": "", "": ""} {
		if got := outcomeOf(in); got != want {
			t.Errorf("outcomeOf(%q) = %q", in, got)
		}
	}
}
