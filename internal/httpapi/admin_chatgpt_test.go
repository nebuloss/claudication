package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"claudication/internal/httpapi/admin"
)

// fakeChatGPT is auth.openai.com and chatgpt.com, as the admin screens reach
// them: the device code, its approval, the code exchange, revocation, the
// usage poll and a test turn. Installed on the gateway's own client, as
// fakeAnthropic is, so nothing leaves the process.
type fakeChatGPT struct {
	mu sync.Mutex
	// approved is how many polls answer pending before one approves.
	approved int
	polls    int
	// accountID and email are who approves.
	accountID, email, plan string
	revoked                []string
	exchanges              []url.Values
}

func unsignedJWT(claims map[string]any) string {
	raw, _ := json.Marshal(claims)
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(raw) + ".x"
}

func (f *fakeChatGPT) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body []byte
	if r.Body != nil { // an outgoing GET carries none
		body, _ = io.ReadAll(r.Body)
	}
	switch r.URL.Host + r.URL.Path {
	case "auth.openai.com/api/accounts/deviceauth/usercode":
		_, _ = io.WriteString(w, `{"device_auth_id":"dev-1","user_code":"WXYZ-0001","interval":"5"}`)
	case "auth.openai.com/api/accounts/deviceauth/token":
		f.polls++
		if f.polls <= f.approved {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, `{"authorization_code":"code-1","code_verifier":"verifier-1"}`)
	case "auth.openai.com/oauth/token":
		form, _ := url.ParseQuery(string(body))
		f.exchanges = append(f.exchanges, form)
		id := unsignedJWT(map[string]any{"email": f.email, "https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": f.accountID, "chatgpt_plan_type": f.plan}})
		_, _ = fmt.Fprintf(w, `{"access_token":"chatgpt-access-%s","refresh_token":"chatgpt-refresh-%s","id_token":%q,"expires_in":3600}`,
			f.accountID, f.accountID, id)
	case "auth.openai.com/oauth/revoke":
		form, _ := url.ParseQuery(string(body))
		f.revoked = append(f.revoked, form.Get("token"))
	case "chatgpt.com/backend-api/wham/usage":
		_, _ = io.WriteString(w, `{"plan_type":"plus","rate_limit":{"allowed":true,"limit_reached":false,
			"primary_window":{"used_percent":21,"limit_window_seconds":18000,"reset_at":1792446828},
			"secondary_window":{"used_percent":3,"limit_window_seconds":604800,"reset_at":1793033628}}}`)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeChatGPT) install(srv *Server) {
	srv.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		f.ServeHTTP(rec, r)
		return rec.Result(), nil
	})
}

type deviceStart struct {
	Provider        string `json:"provider"`
	Flow            string `json:"flow"`
	State           string `json:"state"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	IntervalS       int    `json:"interval_s"`
}

// A ChatGPT account is added by device code: the code and the page to enter it
// on, pending until it is approved, then stored with the identity the sign-in
// named — address, workspace, plan — and its usage read before answering.
func TestChatGPTAccountConnectsByDeviceCode(t *testing.T) {
	srv, st, _ := newTestServer(t)
	fake := &fakeChatGPT{approved: 2, accountID: "acct-personal", email: "me@example.com", plan: "plus"}
	fake.install(srv)
	base, c := adminAPI(t, srv)

	status, body := call(t, base, http.MethodPost, "/admin/accounts/oauth/start", `{"provider":"openai"}`, c)
	if status != http.StatusOK {
		t.Fatalf("start: %d %s", status, body)
	}
	start := decode[deviceStart](t, body)
	if start.Flow != "device" || start.UserCode != "WXYZ-0001" ||
		start.VerificationURI != "https://auth.openai.com/codex/device" || start.IntervalS != 5 || start.State == "" {
		t.Fatalf("start = %+v", start)
	}

	poll := func(state string) (int, []byte) {
		return call(t, base, http.MethodPost, "/admin/accounts/oauth/poll",
			`{"provider":"openai","state":"`+state+`"}`, c)
	}
	for i := 0; i < 2; i++ {
		if status, body := poll(start.State); status != http.StatusAccepted || !strings.Contains(string(body), "pending") {
			t.Fatalf("poll %d: %d %s, want pending", i, status, body)
		}
	}
	status, body = poll(start.State)
	if status != http.StatusOK {
		t.Fatalf("approved poll: %d %s", status, body)
	}
	got := decode[struct {
		Account admin.AccountJSON `json:"account"`
		Renewed bool              `json:"renewed"`
	}](t, body)
	a := got.Account
	if a.Provider != "openai" || a.Email != "me@example.com" || a.Plan != "plus" || got.Renewed {
		t.Errorf("account = %+v", a)
	}
	if a.Quota == nil || a.Quota.FiveHourUtil != 21 || a.Quota.SevenDayUtil != 3 || len(a.Quota.Limits) != 2 ||
		a.Quota.Limits[0].Title != "5-hour limit" || a.Quota.Limits[1].WindowSeconds != 604800 {
		t.Errorf("quota = %+v", a.Quota)
	}
	stored, err := st.Account(context.Background(), a.ID)
	if err != nil || stored.AccountUUID != "acct-personal" {
		t.Errorf("stored = %+v, %v", stored, err)
	}
	if ex := fake.exchanges[0]; ex.Get("redirect_uri") != "https://auth.openai.com/deviceauth/callback" ||
		ex.Get("code_verifier") != "verifier-1" {
		t.Errorf("exchange = %v", ex)
	}

	// Spent: the same attempt cannot store twice.
	if status, body := poll(start.State); status != http.StatusBadRequest || errType(t, body) != "unknown_state" {
		t.Errorf("replay: %d %s", status, body)
	}

	// The same address signing in to another workspace is another account.
	fake.mu.Lock()
	fake.polls, fake.approved, fake.accountID, fake.plan = 0, 0, "acct-team", "team"
	fake.mu.Unlock()
	_, body = call(t, base, http.MethodPost, "/admin/accounts/oauth/start", `{"provider":"openai"}`, c)
	_, body = poll(decode[deviceStart](t, body).State)
	team := decode[struct {
		Account admin.AccountJSON `json:"account"`
	}](t, body).Account
	if team.ID == "" || team.ID == a.ID || team.Plan != "team" || team.Email != "me@example.com" {
		t.Errorf("second workspace = %+v, first %s", team, a.ID)
	}

	// And the first, signed in again, is renewed in place.
	fake.mu.Lock()
	fake.polls, fake.accountID, fake.plan = 0, "acct-personal", "pro"
	fake.mu.Unlock()
	_, body = call(t, base, http.MethodPost, "/admin/accounts/oauth/start", `{"provider":"openai"}`, c)
	_, body = poll(decode[deviceStart](t, body).State)
	again := decode[struct {
		Account admin.AccountJSON `json:"account"`
		Renewed bool              `json:"renewed"`
	}](t, body)
	if again.Account.ID != a.ID || !again.Renewed || again.Account.Plan != "pro" {
		t.Errorf("signed in again = %+v", again)
	}
	if n := len(must(st.ListAccounts(context.Background()))); n != 2 {
		t.Errorf("%d accounts, want the two workspaces", n)
	}

	// A paste is not how this provider signs in.
	if status, body := call(t, base, http.MethodPost, "/admin/accounts/oauth/complete",
		`{"provider":"openai","state":"x","redirect_url":"code"}`, c); status != http.StatusBadRequest ||
		errType(t, body) != "unsupported_provider" {
		t.Errorf("paste for ChatGPT: %d %s", status, body)
	}

	// Deleting hands the refresh token back to OpenAI, not to Anthropic.
	if status, body := call(t, base, http.MethodDelete, "/admin/accounts/"+team.ID, "", c); status != http.StatusOK {
		t.Fatalf("delete: %d %s", status, body)
	}
	fake.mu.Lock()
	revoked := append([]string(nil), fake.revoked...)
	fake.mu.Unlock()
	if len(revoked) != 1 || revoked[0] != "chatgpt-refresh-acct-team" {
		t.Errorf("revoked %v", revoked)
	}
}

// A reconnect that comes back as another workspace changes nothing, as for
// Claude.
func TestChatGPTReconnectAsAnotherWorkspaceChangesNothing(t *testing.T) {
	srv, st, _ := newTestServer(t)
	fake := &fakeChatGPT{accountID: "acct-team", email: "me@example.com", plan: "team"}
	fake.install(srv)
	base, c := adminAPI(t, srv)
	_, body := call(t, base, http.MethodPost, "/admin/accounts/oauth/start", `{"provider":"openai"}`, c)
	_, body = call(t, base, http.MethodPost, "/admin/accounts/oauth/poll",
		`{"provider":"openai","state":"`+decode[deviceStart](t, body).State+`"}`, c)
	first := decode[struct {
		Account admin.AccountJSON `json:"account"`
	}](t, body).Account

	fake.mu.Lock()
	fake.polls, fake.accountID = 0, "acct-personal"
	fake.mu.Unlock()
	_, body = call(t, base, http.MethodPost, "/admin/accounts/oauth/start",
		`{"provider":"openai","account_id":"`+first.ID+`"}`, c)
	status, body := call(t, base, http.MethodPost, "/admin/accounts/oauth/poll",
		`{"provider":"openai","state":"`+decode[deviceStart](t, body).State+`"}`, c)
	if status != http.StatusConflict || errType(t, body) != "wrong_account" {
		t.Errorf("reconnect as another workspace: %d %s", status, body)
	}
	if n := len(must(st.ListAccounts(context.Background()))); n != 1 {
		t.Errorf("%d accounts after a refused reconnect", n)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
