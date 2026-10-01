package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"claudication/internal/httpapi/admin"
	"claudication/internal/pool"
	"claudication/internal/provider/anthropic"
	"claudication/internal/store"
)

// roundTripFunc lets a test stand in for the network.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fakeAnthropic answers every call the gateway makes on its own behalf — token
// exchange and refresh, revocation, the usage poll, the credential probe and
// the model list — by path, whatever host the provider package names.
//
// Those hosts are constants in internal/provider/anthropic, so the admin
// screens that drive them could only be tested against the real provider.
// Swapping the transport on the gateway's own client reaches every one of
// them without touching production code, and nothing leaves the process.
type fakeAnthropic struct {
	mu          sync.Mutex
	revoked     []string
	revokeFails bool
	usageFails  bool
	modelsFail  bool
	modelCalls  int
	probeTokens []string
}

// The fake is read by the test while the gateway's handlers write to it, so
// every access goes through the lock.
func (f *fakeAnthropic) lastProbe() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.probeTokens) == 0 {
		return ""
	}
	return f.probeTokens[len(f.probeTokens)-1]
}

func (f *fakeAnthropic) revokedTokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.revoked...)
}

func (f *fakeAnthropic) models() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.modelCalls
}

func (f *fakeAnthropic) set(fn func(*fakeAnthropic)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeAnthropic) install(srv *Server) {
	srv.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		f.ServeHTTP(rec, r)
		return rec.Result(), nil
	})
}

func (f *fakeAnthropic) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	var body []byte
	if r.Body != nil { // an outgoing GET carries none
		body, _ = io.ReadAll(r.Body)
	}
	w.Header().Set("Content-Type", "application/json")

	switch r.URL.Path {
	case "/v1/oauth/token":
		var req map[string]string
		_ = json.Unmarshal(body, &req)
		switch {
		case req["grant_type"] == "authorization_code" && req["code"] == "good" &&
			req["code_verifier"] != "" && req["redirect_uri"] == anthropic.RedirectManual:
			_, _ = io.WriteString(w, `{"access_token":"new-access","refresh_token":"new-refresh",
				"expires_in":3600,"refresh_token_expires_in":2592000,
				"account":{"email_address":"new@example.com","uuid":"uuid-1"}}`)
		case req["grant_type"] == "refresh_token" && req["refresh_token"] != "dead-refresh":
			_, _ = io.WriteString(w, `{"access_token":"refreshed-access","refresh_token":"refreshed-refresh","expires_in":3600}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
		}
	case "/v1/oauth/token/revoke":
		var req map[string]string
		_ = json.Unmarshal(body, &req)
		f.revoked = append(f.revoked, req["token"])
		if f.revokeFails {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	case "/api/oauth/usage":
		if f.usageFails || bearer == "no-usage" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, `{"five_hour":{"utilization":42,"resets_at":"2026-10-01T15:00:00Z"},
			"seven_day":{"utilization":10,"resets_at":"2026-10-07T00:00:00Z"},
			"limits":[{"kind":"session","percent":42,"severity":"normal","is_active":true,"resets_at":"2026-10-01T15:00:00Z"},
			{"kind":"weekly_scoped","percent":55,"severity":"warning","scope":{"model":{"display_name":"Fable"}}}]}`)
	case "/v1/messages":
		f.probeTokens = append(f.probeTokens, bearer)
		if bearer != "test-access" && bearer != "refreshed-access" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid token"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"model":"claude-haiku-4-5","stop_reason":"end_turn",
			"content":[{"type":"text","text":" pong "}],"usage":{"input_tokens":3,"output_tokens":1}}`)
	case "/v1/models":
		f.modelCalls++
		if f.modelsFail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"claude-opus-5","type":"model"}]}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// adminAPI serves the combined surface over httptest, without Run: no usage
// poller or sweeper runs behind the test's back, so what the store holds is
// only what the requests put there. Returns the base URL and a signed-in
// session.
func adminAPI(t *testing.T, srv *Server) (string, *http.Cookie) {
	t.Helper()
	ts := httptest.NewServer(srv.routes(role{gateway: true, admin: true}))
	t.Cleanup(ts.Close)
	return ts.URL, claim(t, ts.URL)
}

// call sends one admin request and returns its status and body.
func call(t *testing.T, base, method, path, body string, c *http.Cookie) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c != nil {
		req.AddCookie(c)
	}
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, b
}

func decode[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return v
}

// errType is the type field of the gateway's error envelope.
func errType(t *testing.T, b []byte) string {
	t.Helper()
	return decode[struct {
		Error struct{ Type string } `json:"error"`
	}](t, b).Error.Type
}

// addAccount connects one account with the given tokens and expiry.
func addAccount(t *testing.T, srv *Server, st *store.Store, email, access, refresh string, expires time.Time) store.Account {
	t.Helper()
	a, err := st.UpsertAccount(context.Background(), srv.sealer,
		store.Account{Provider: "anthropic", Email: email, ExpiresAt: expires},
		store.Tokens{AccessToken: access, RefreshToken: refresh})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

type accountsList struct {
	Accounts   []admin.AccountJSON `json:"accounts"`
	WindowDays int                 `json:"window_days"`
	PollS      int                 `json:"usage_poll_s"`
}

// The accounts screen is where an operator decides which account to fix, so
// every state it shows has to come from the source that decides it: quota from
// the last poll, "serving" and cooldowns from the pool, traffic from the usage
// log, and re-authorisation from the refresh token's fate.
func TestAccountListReportsWhatTheOperatorActsOn(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, c := adminAPI(t, srv)
	ctx := context.Background()

	first := addAccount(t, srv, st, "first@example.com", "a1", "r1", time.Now().Add(time.Hour))
	second := addAccount(t, srv, st, "second@example.com", "a2", "r2", time.Now().Add(time.Hour))
	dead := addAccount(t, srv, st, "dead@example.com", "a3", "r3", time.Now().Add(-time.Hour))

	// First has a quota and some traffic; it is cooling down, so the pool
	// serves the second. The third's refresh token was refused for good.
	if err := st.SetAccountQuota(ctx, first.ID, store.AccountQuota{
		UpdatedAt: time.Now(), FiveHourUtil: 42, SevenDayUtil: 10,
		FiveHourReset: time.Now().Add(time.Hour), SevenDayReset: time.Now().Add(48 * time.Hour),
		FiveHourStatus: "allowed", SevenDayStatus: "allowed",
		Detail: `[{"kind":"weekly_scoped","percent":55,"severity":"warning","resets_at":"2026-10-07T00:00:00Z",
			"scope":{"model":{"display_name":"Fable"}}}]`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordUsage(ctx, store.UsageEvent{
		At: time.Now(), AccountID: first.ID, Path: "/v1/messages", Status: 200,
		InputTokens: 100, OutputTokens: 20, CacheReadTokens: 5,
	}); err != nil {
		t.Fatal(err)
	}
	srv.pool.ReportFailure(first.ID, pool.FailureRateLimit, "429")
	if err := st.MarkRefreshDead(ctx, dead.ID, "invalid_grant"); err != nil {
		t.Fatal(err)
	}

	status, body := call(t, base, http.MethodGet, "/admin/accounts", "", c)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	got := decode[accountsList](t, body)
	if len(got.Accounts) != 3 {
		t.Fatalf("got %d accounts", len(got.Accounts))
	}
	if got.WindowDays <= 0 || got.PollS <= 0 {
		t.Errorf("window_days = %d, usage_poll_s = %d; the UI paces itself off these", got.WindowDays, got.PollS)
	}
	byEmail := map[string]admin.AccountJSON{}
	for i, a := range got.Accounts {
		if a.Position != i {
			t.Errorf("%s: position %d at index %d", a.Email, a.Position, i)
		}
		byEmail[a.Email] = a
	}

	f := byEmail["first@example.com"]
	if f.Serving || f.CoolingUntil == "" {
		t.Errorf("a cooling account: serving=%v cooling_until=%q", f.Serving, f.CoolingUntil)
	}
	if f.Requests != 1 || f.Tokens != 125 {
		t.Errorf("traffic = %d requests, %d tokens; want 1 and 125", f.Requests, f.Tokens)
	}
	if f.Quota == nil || f.Quota.FiveHourUtil != 42 || f.Quota.SevenDayReset == "" || !f.Quota.Allowed {
		t.Fatalf("quota = %+v", f.Quota)
	}
	if len(f.Quota.Limits) != 1 || f.Quota.Limits[0].Title != "Current week (Fable)" ||
		f.Quota.Limits[0].ResetsAt == "" {
		t.Errorf("limits = %+v, want the scoped row titled the way the client titles it", f.Quota.Limits)
	}

	if !byEmail["second@example.com"].Serving {
		t.Error("the next account in line is not marked serving while the first cools")
	}
	if s := byEmail["second@example.com"]; s.Quota != nil {
		t.Errorf("an account never polled shows a quota: %+v", s.Quota)
	}

	d := byEmail["dead@example.com"]
	if !d.NeedsReauth || d.RefreshDeadAt == "" || !d.Expired {
		t.Errorf("a refused refresh token: needs_reauth=%v dead_at=%q expired=%v",
			d.NeedsReauth, d.RefreshDeadAt, d.Expired)
	}
	_ = second
}

// Priority order decides which subscription is spent first. The whole list is
// sent and the answer is the list as stored, so the UI cannot drift from it.
func TestReorderAccounts(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, c := adminAPI(t, srv)
	a := addAccount(t, srv, st, "a@example.com", "a", "ra", time.Now().Add(time.Hour))
	b := addAccount(t, srv, st, "b@example.com", "b", "rb", time.Now().Add(time.Hour))

	status, body := call(t, base, http.MethodPost, "/admin/accounts/order",
		`{"ids":["`+b.ID+`","`+a.ID+`"]}`, c)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	got := decode[accountsList](t, body)
	if len(got.Accounts) != 2 || got.Accounts[0].ID != b.ID || !got.Accounts[0].Serving {
		t.Fatalf("after reorder: %+v", got.Accounts)
	}
	// And the pool agrees: the reordered head is what serves.
	if s, _ := srv.pool.Status(context.Background(), "anthropic"); s.Serving != b.ID {
		t.Errorf("pool serves %s, want %s", s.Serving, b.ID)
	}

	for _, bad := range []string{`{"ids":[]}`, `{"ids":`, `nope`} {
		if status, body := call(t, base, http.MethodPost, "/admin/accounts/order", bad, c); status != http.StatusBadRequest {
			t.Errorf("%s: status = %d: %s", bad, status, body)
		}
	}
}

// Connecting an account is the one flow that needs a browser, and it has to
// survive the ways an operator gets it wrong: a paste from another attempt, a
// stale attempt, a code the provider refuses. A refused code leaves the
// attempt open so the corrected paste can be retried without starting over.
func TestOAuthConnectsAnAccount(t *testing.T) {
	srv, st, _ := newTestServer(t)
	fake := &fakeAnthropic{}
	fake.install(srv)
	base, c := adminAPI(t, srv)

	if status, body := call(t, base, http.MethodPost, "/admin/accounts/oauth/start",
		`{"provider":"openai"}`, c); status != http.StatusBadRequest || errType(t, body) != "unsupported_provider" {
		t.Errorf("unsupported provider: %d %s", status, body)
	}
	if status, _ := call(t, base, http.MethodPost, "/admin/accounts/oauth/start", `{`, c); status != http.StatusBadRequest {
		t.Errorf("malformed start: %d", status)
	}

	status, body := call(t, base, http.MethodPost, "/admin/accounts/oauth/start", `{}`, c)
	if status != http.StatusOK {
		t.Fatalf("start: %d %s", status, body)
	}
	start := decode[struct {
		Provider    string `json:"provider"`
		State       string `json:"state"`
		AuthURL     string `json:"auth_url"`
		RedirectURI string `json:"redirect_uri"`
		ExpiresAt   string `json:"expires_at"`
	}](t, body)
	if start.Provider != "anthropic" || start.State == "" || start.RedirectURI != anthropic.RedirectManual {
		t.Fatalf("start = %+v", start)
	}
	u, err := url.Parse(start.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("state") != start.State || u.Query().Get("code_challenge") == "" {
		t.Errorf("auth_url %s does not carry the attempt's state and PKCE challenge", start.AuthURL)
	}

	complete := func(state, redirect string) (int, []byte) {
		b, _ := json.Marshal(map[string]string{"state": state, "redirect_url": redirect})
		return call(t, base, http.MethodPost, "/admin/accounts/oauth/complete", string(b), c)
	}

	for name, tc := range map[string]struct {
		state, redirect, want string
		status                int
	}{
		"nothing pasted":  {start.State, "", "invalid_callback", http.StatusBadRequest},
		"other attempt":   {start.State, "good#someone-else", "state_mismatch", http.StatusBadRequest},
		"unknown attempt": {"never-issued", "good", "unknown_state", http.StatusBadRequest},
		"refused code":    {start.State, "bad#" + start.State, "exchange_failed", http.StatusBadGateway},
	} {
		status, body := complete(tc.state, tc.redirect)
		if status != tc.status || errType(t, body) != tc.want {
			t.Errorf("%s: %d %s, want %d %s", name, status, body, tc.status, tc.want)
		}
	}
	if status, _ := call(t, base, http.MethodPost, "/admin/accounts/oauth/complete", `[`, c); status != http.StatusBadRequest {
		t.Errorf("malformed complete: %d", status)
	}

	// The refused code did not spend the attempt.
	status, body = complete(start.State, "https://platform.claude.com/oauth/code/callback?code=good&state="+start.State)
	if status != http.StatusOK {
		t.Fatalf("complete: %d %s", status, body)
	}
	got := decode[struct {
		Account admin.AccountJSON `json:"account"`
	}](t, body).Account
	if got.Email != "new@example.com" || got.ReauthDaysLeft == nil || *got.ReauthDaysLeft != 30 {
		t.Errorf("account = %+v", got)
	}
	// Usage was read before answering, so a new account does not show blank.
	if got.Quota == nil || got.Quota.FiveHourUtil != 42 || len(got.Quota.Limits) != 2 {
		t.Errorf("quota = %+v, want the first poll's figures", got.Quota)
	}

	tokens, err := st.AccountTokens(context.Background(), srv.sealer, got.ID)
	if err != nil || tokens.AccessToken != "new-access" || tokens.RefreshToken != "new-refresh" {
		t.Errorf("stored tokens = %+v, %v", tokens, err)
	}

	// Redeemed: the verifier is spent and the same paste cannot run twice.
	if status, body := complete(start.State, "good"); status != http.StatusBadRequest || errType(t, body) != "unknown_state" {
		t.Errorf("replay: %d %s", status, body)
	}
}

// "Test" answers whether this account can serve right now, so an expired
// access token is refreshed first rather than reported as a failure — and the
// verdict is written to the account where the list shows it.
func TestTestAccountRefreshesAndRecordsTheVerdict(t *testing.T) {
	srv, st, _ := newTestServer(t)
	fake := &fakeAnthropic{}
	fake.install(srv)
	base, c := adminAPI(t, srv)
	ctx := context.Background()

	expired := addAccount(t, srv, st, "old@example.com", "stale-access", "test-refresh", time.Now().Add(-time.Hour))
	status, body := call(t, base, http.MethodPost, "/admin/accounts/"+expired.ID+"/test", `{"model":"claude-haiku-4-5"}`, c)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	res := decode[anthropic.ProbeResult](t, body)
	if !res.OK || res.Reply != "pong" || res.Model != "claude-haiku-4-5" {
		t.Errorf("probe = %+v", res)
	}
	if got := fake.lastProbe(); got != "refreshed-access" {
		t.Errorf("probed with %q, want the refreshed token", got)
	}
	if a, _ := st.Account(ctx, expired.ID); a.LastUsedAt == nil || a.LastError != "" {
		t.Errorf("a passing test was not recorded: %+v", a)
	}

	broken := addAccount(t, srv, st, "broken@example.com", "revoked-access", "r", time.Now().Add(time.Hour))
	status, body = call(t, base, http.MethodPost, "/admin/accounts/"+broken.ID+"/test", "", c)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	if res := decode[anthropic.ProbeResult](t, body); res.OK || res.Status != http.StatusUnauthorized ||
		!strings.Contains(res.Error, "invalid token") {
		t.Errorf("a refused probe = %+v, want the upstream's own words", res)
	}
	if a, _ := st.Account(ctx, broken.ID); !strings.Contains(a.LastError, "invalid token") {
		t.Errorf("last_error = %q", a.LastError)
	}

	if status, _ := call(t, base, http.MethodPost, "/admin/accounts/nope/test", "", c); status != http.StatusNotFound {
		t.Errorf("unknown account: %d", status)
	}
}

// A forced refresh goes through the pool and reports the provider's refusal
// as an upstream failure, not as success with stale tokens.
func TestRefreshAccount(t *testing.T) {
	srv, st, _ := newTestServer(t)
	(&fakeAnthropic{}).install(srv)
	base, c := adminAPI(t, srv)
	ctx := context.Background()

	ok := addAccount(t, srv, st, "ok@example.com", "a", "test-refresh", time.Now().Add(time.Hour))
	status, body := call(t, base, http.MethodPost, "/admin/accounts/"+ok.ID+"/refresh", "", c)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	if a := decode[struct {
		Account admin.AccountJSON `json:"account"`
	}](t, body).Account; a.LastRefreshAt == "" {
		t.Errorf("refreshed account shows no last_refresh_at: %+v", a)
	}
	if tok, _ := st.AccountTokens(ctx, srv.sealer, ok.ID); tok.RefreshToken != "refreshed-refresh" {
		t.Errorf("the rotated refresh token was not kept: %+v", tok)
	}

	dead := addAccount(t, srv, st, "dead@example.com", "a", "dead-refresh", time.Now().Add(time.Hour))
	if status, body := call(t, base, http.MethodPost, "/admin/accounts/"+dead.ID+"/refresh", "", c); status != http.StatusBadGateway ||
		errType(t, body) != "refresh_failed" {
		t.Errorf("refused refresh: %d %s", status, body)
	}
	if a, _ := st.Account(ctx, dead.ID); !a.NeedsReauth() {
		t.Error("an invalid_grant refresh did not mark the account for re-authorisation")
	}

	if status, _ := call(t, base, http.MethodPost, "/admin/accounts/nope/refresh", "", c); status != http.StatusNotFound {
		t.Errorf("unknown account: %d", status)
	}
}

// The refresh-usage button reads the subscription now, so the figure in the
// answer is the fresh one; a failed read is an upstream error, not stale data
// dressed as new.
func TestRefreshUsageOnDemand(t *testing.T) {
	srv, st, _ := newTestServer(t)
	(&fakeAnthropic{}).install(srv)
	base, c := adminAPI(t, srv)

	a := addAccount(t, srv, st, "a@example.com", "test-access", "r", time.Now().Add(time.Hour))
	status, body := call(t, base, http.MethodPost, "/admin/accounts/"+a.ID+"/usage", "", c)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	got := decode[struct {
		Account admin.AccountJSON `json:"account"`
	}](t, body).Account
	if got.Quota == nil || got.Quota.SevenDayUtil != 10 || got.Quota.FiveHourStatus != "allowed" {
		t.Errorf("quota = %+v", got.Quota)
	}

	silent := addAccount(t, srv, st, "s@example.com", "no-usage", "r", time.Now().Add(time.Hour))
	if status, body := call(t, base, http.MethodPost, "/admin/accounts/"+silent.ID+"/usage", "", c); status != http.StatusBadGateway {
		t.Errorf("failing poll: %d %s", status, body)
	}
	if status, _ := call(t, base, http.MethodPost, "/admin/accounts/nope/usage", "", c); status != http.StatusBadGateway {
		t.Errorf("unknown account: %d", status)
	}
}

// Pausing over the API takes the account out of rotation and back, and says
// so in the answer.
func TestPauseAccountOverTheAPI(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, c := adminAPI(t, srv)
	a := addAccount(t, srv, st, "a@example.com", "x", "r", time.Now().Add(time.Hour))

	status, body := call(t, base, http.MethodPost, "/admin/accounts/"+a.ID+"/disabled", `{"disabled":true}`, c)
	if status != http.StatusOK || !decode[admin.AccountJSON](t, body).Disabled {
		t.Fatalf("pause: %d %s", status, body)
	}
	if s, _ := srv.pool.Status(context.Background(), "anthropic"); s.Serving != "" {
		t.Errorf("a paused account still serves")
	}
	status, body = call(t, base, http.MethodPost, "/admin/accounts/"+a.ID+"/disabled", `{"disabled":false}`, c)
	if status != http.StatusOK || decode[admin.AccountJSON](t, body).Disabled {
		t.Fatalf("resume: %d %s", status, body)
	}
	if status, _ := call(t, base, http.MethodPost, "/admin/accounts/nope/disabled", `{"disabled":true}`, c); status != http.StatusNotFound {
		t.Errorf("unknown account: %d", status)
	}
	if status, _ := call(t, base, http.MethodPost, "/admin/accounts/"+a.ID+"/disabled", `x`, c); status != http.StatusBadRequest {
		t.Errorf("malformed body: %d", status)
	}
}

// Deleting hands the refresh token back upstream before forgetting it — and an
// upstream that will not take it back must not keep the account alive here.
func TestDeleteAccountRevokesThenForgets(t *testing.T) {
	srv, st, _ := newTestServer(t)
	fake := &fakeAnthropic{}
	fake.install(srv)
	base, c := adminAPI(t, srv)
	ctx := context.Background()

	a := addAccount(t, srv, st, "a@example.com", "x", "refresh-a", time.Now().Add(time.Hour))
	if status, body := call(t, base, http.MethodDelete, "/admin/accounts/"+a.ID, "", c); status != http.StatusOK {
		t.Fatalf("delete: %d %s", status, body)
	}
	if got := fake.revokedTokens(); len(got) != 1 || got[0] != "refresh-a" {
		t.Errorf("revoked %v, want the account's refresh token", got)
	}
	if _, err := st.Account(ctx, a.ID); err != store.ErrAccountNotFound {
		t.Errorf("account still stored: %v", err)
	}

	fake.set(func(f *fakeAnthropic) { f.revokeFails = true })
	b := addAccount(t, srv, st, "b@example.com", "x", "refresh-b", time.Now().Add(time.Hour))
	if status, body := call(t, base, http.MethodDelete, "/admin/accounts/"+b.ID, "", c); status != http.StatusOK {
		t.Fatalf("delete with a failing revoke: %d %s", status, body)
	}
	if _, err := st.Account(ctx, b.ID); err != store.ErrAccountNotFound {
		t.Errorf("a failed revoke kept the account: %v", err)
	}

	if status, _ := call(t, base, http.MethodDelete, "/admin/accounts/"+a.ID, "", c); status != http.StatusNotFound {
		t.Errorf("second delete: %d", status)
	}
}

// Every one of these reads or changes credentials, so none may answer without
// a session — including the ones the older keys test does not list.
func TestAccountEndpointsRequireASession(t *testing.T) {
	srv, _, _ := newTestServer(t)
	base, _ := adminAPI(t, srv)
	stale := &http.Cookie{Name: admin.SessionCookie, Value: "not-a-session"}
	for _, ep := range []struct{ method, path string }{
		{http.MethodGet, "/admin/accounts"},
		{http.MethodPost, "/admin/accounts/order"},
		{http.MethodPost, "/admin/accounts/oauth/start"},
		{http.MethodPost, "/admin/accounts/oauth/complete"},
		{http.MethodPost, "/admin/accounts/x/test"},
		{http.MethodPost, "/admin/accounts/x/refresh"},
		{http.MethodPost, "/admin/accounts/x/usage"},
		{http.MethodPost, "/admin/accounts/x/disabled"},
		{http.MethodDelete, "/admin/accounts/x"},
		{http.MethodGet, "/admin/config"},
		{http.MethodGet, "/admin/models"},
		{http.MethodPost, "/admin/surfaces/openai"},
		{http.MethodGet, "/admin/overview"},
		{http.MethodGet, "/admin/usage"},
		{http.MethodGet, "/admin/chats"},
		{http.MethodGet, "/admin/chats/x"},
		{http.MethodPost, "/admin/chat-titles"},
		{http.MethodPost, "/admin/fit-images"},
		{http.MethodPost, "/admin/docs"},
		{http.MethodGet, "/admin/requests"},
		{http.MethodGet, "/admin/requests/facets"},
		{http.MethodGet, "/admin/requests/export"},
		{http.MethodPatch, "/admin/keys/x"},
	} {
		for _, c := range []*http.Cookie{nil, stale} {
			status, body := call(t, base, ep.method, ep.path, `{}`, c)
			if status != http.StatusUnauthorized || errType(t, body) != "authentication_error" {
				t.Errorf("%s %s (cookie %v): %d %s", ep.method, ep.path, c != nil, status, body)
			}
		}
	}
}
