package google

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"claudication/internal/oauth"
	"claudication/internal/provider"
	"claudication/internal/testenv"
)

// The suite needs no network; see testenv. Every call the package makes goes
// to the fake below, through a client whose transport is the fake.
//
// What was measured is in testdata (scripts/probe-antigravity.py,
// 2026-10-23), and the tests that can read it do; the rest of the bodies
// here are shaped like it.
func TestMain(m *testing.M) { testenv.Offline(m) }

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func clientFor(h http.Handler) *http.Client {
	return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Result(), nil
	})}
}

// fakeGoogle answers the token, userinfo and Code Assist endpoints, by host
// and path, recording what it was sent.
type fakeGoogle struct {
	mu      sync.Mutex
	forms   []url.Values
	calls   map[string][]map[string]any
	load    string
	onboard string
	token   func(url.Values) (int, string)
}

func (f *fakeGoogle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// A GET handed straight to a transport has no body at all.
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	key := r.URL.Host + r.URL.Path
	switch key {
	case "oauth2.googleapis.com/token":
		form, _ := url.ParseQuery(string(body))
		f.forms = append(f.forms, form)
		status, out := http.StatusOK, `{"access_token":"ya29.access","refresh_token":"1//refresh","expires_in":3599}`
		if f.token != nil {
			status, out = f.token(form)
		}
		w.WriteHeader(status)
		io.WriteString(w, out)
	case "www.googleapis.com/oauth2/v2/userinfo":
		if r.Header.Get("Authorization") != "Bearer ya29.access" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"email":"someone@example.com"}`)
	default:
		if f.calls == nil {
			f.calls = map[string][]map[string]any{}
		}
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		f.calls[key] = append(f.calls[key], in)
		switch key {
		case "daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
			io.WriteString(w, f.load)
		case "daily-cloudcode-pa.googleapis.com/v1internal:onboardUser":
			io.WriteString(w, f.onboard)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

// fixture is one of scripts/probe-antigravity.py's records, scrubbed.
type fixture struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

func readFixture(t *testing.T, name string) fixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return f
}

// streamBody is a recorded stream, kept as the string it was read as.
func (f fixture) streamBody(t *testing.T) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(f.Body, &s); err != nil {
		t.Fatalf("not a stream: %v", err)
	}
	return s
}

func TestAuthURLAsksForAnOfflineConsent(t *testing.T) {
	raw := AuthURL("st", oauth.PKCE{Challenge: "ch"}, Redirect, "someone@example.com")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"client_id":             ClientID,
		"redirect_uri":          Redirect,
		"access_type":           "offline",
		"prompt":                "consent",
		"code_challenge":        "ch",
		"code_challenge_method": "S256",
		"state":                 "st",
		"login_hint":            "someone@example.com",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if !strings.Contains(q.Get("scope"), "cloud-platform") || !strings.Contains(q.Get("scope"), "experimentsandconfigs") {
		t.Errorf("scope = %q", q.Get("scope"))
	}
}

func TestSignInOnboardsAFreeAccount(t *testing.T) {
	f := &fakeGoogle{
		load:    `{"allowedTiers":[{"id":"legacy-tier"},{"id":"free-tier","name":"Gemini Code Assist for individuals","isDefault":true}]}`,
		onboard: `{"name":"operations/x","done":true,"response":{"cloudaicompanionProject":{"id":"managed-123"}}}`,
	}
	res, err := Accounts{}.Exchange(context.Background(), clientFor(f), "4/code", "verifier", "", Redirect)
	if err != nil {
		t.Fatal(err)
	}
	if res.Email != "someone@example.com" || res.AccountUUID != "managed-123" || res.RefreshToken != "1//refresh" {
		t.Errorf("result = %+v", res)
	}
	if res.Plan != "Gemini Code Assist for individuals" {
		t.Errorf("plan = %q", res.Plan)
	}
	form := f.forms[0]
	if form.Get("code_verifier") != "verifier" || form.Get("client_secret") == "" || form.Get("redirect_uri") != Redirect {
		t.Errorf("exchange form = %v", form)
	}
	onboard := f.calls["daily-cloudcode-pa.googleapis.com/v1internal:onboardUser"][0]
	if onboard["tierId"] != "free-tier" {
		t.Errorf("onboarded onto %v", onboard["tierId"])
	}
	// The free tier's project is Google's to make: naming one is refused.
	if _, named := onboard["cloudaicompanionProject"]; named {
		t.Errorf("onboarding named a project: %v", onboard)
	}
}

func TestSignInKeepsAnOnboardedAccountsProject(t *testing.T) {
	f := &fakeGoogle{
		load: `{"currentTier":{"id":"standard-tier","name":"Standard"},"paidTier":{"id":"g1-pro-tier","name":"Google AI Pro"},"cloudaicompanionProject":"proj-9"}`,
	}
	res, err := Accounts{}.Exchange(context.Background(), clientFor(f), "4/code", "v", "", Redirect)
	if err != nil {
		t.Fatal(err)
	}
	if res.AccountUUID != "proj-9" || res.Plan != "Google AI Pro" {
		t.Errorf("result = %+v", res)
	}
	if n := len(f.calls["daily-cloudcode-pa.googleapis.com/v1internal:onboardUser"]); n != 0 {
		t.Errorf("onboarded an account already on a tier, %d times", n)
	}
}

func TestSignInRefusesATierThatNeedsItsOwnProject(t *testing.T) {
	f := &fakeGoogle{
		load: `{"currentTier":{"id":"standard-tier"},"ineligibleTiers":[{"reasonCode":"DASHER_USER","reasonMessage":"Workspace accounts need a project"}]}`,
	}
	_, err := Accounts{}.Exchange(context.Background(), clientFor(f), "4/code", "v", "", Redirect)
	if err == nil || !strings.Contains(err.Error(), "Workspace accounts need a project") {
		t.Fatalf("err = %v", err)
	}
}

func TestSignInPointsAtAVerification(t *testing.T) {
	f := &fakeGoogle{
		load: `{"ineligibleTiers":[{"reasonCode":"VALIDATION_REQUIRED","validationUrl":"https://accounts.google.com/verify"}]}`,
	}
	_, err := Accounts{}.Exchange(context.Background(), clientFor(f), "4/code", "v", "", Redirect)
	if err == nil || !strings.Contains(err.Error(), "https://accounts.google.com/verify") {
		t.Fatalf("err = %v", err)
	}
}

func TestSignInWithoutARefreshTokenFails(t *testing.T) {
	f := &fakeGoogle{token: func(url.Values) (int, string) {
		return http.StatusOK, `{"access_token":"ya29.access","expires_in":3599}`
	}}
	_, err := Accounts{}.Exchange(context.Background(), clientFor(f), "4/code", "v", "", Redirect)
	if err == nil || !strings.Contains(err.Error(), "no refresh token") {
		t.Fatalf("err = %v", err)
	}
}

func TestRefreshKeepsTheTokenAndKnowsAFinalRefusal(t *testing.T) {
	f := &fakeGoogle{token: func(form url.Values) (int, string) {
		if form.Get("refresh_token") == "1//dead" {
			return http.StatusBadRequest, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`
		}
		return http.StatusOK, `{"access_token":"ya29.new","expires_in":3599}`
	}}
	res, err := Refresh(context.Background(), clientFor(f), "1//live")
	if err != nil {
		t.Fatal(err)
	}
	if res.AccessToken != "ya29.new" || res.RefreshToken != "1//live" {
		t.Errorf("result = %+v", res)
	}
	if d := time.Until(res.ExpiresAt); d < 59*time.Minute || d > time.Hour {
		t.Errorf("expires in %v", d)
	}
	if _, err := Refresh(context.Background(), clientFor(f), "1//dead"); !errors.Is(err, oauth.ErrInvalidGrant) {
		t.Errorf("err = %v, want ErrInvalidGrant", err)
	}
	if _, err := Refresh(context.Background(), clientFor(&fakeGoogle{token: func(url.Values) (int, string) {
		return http.StatusInternalServerError, `{"error":"internal_failure"}`
	}}), "1//live"); err == nil || errors.Is(err, oauth.ErrInvalidGrant) {
		t.Errorf("a server failure was read as final: %v", err)
	}
}

func TestStampPutsTheProjectFirst(t *testing.T) {
	cred := provider.Credential{AccountID: `proj-"9"`}
	got := string(Provider{}.Stamp([]byte(`{"model":"gemini-2.5-pro","request":{}}`), cred))
	if got != `{"project":"proj-\"9\"","model":"gemini-2.5-pro","request":{}}` {
		t.Errorf("stamped = %s", got)
	}
	if got := string(Provider{}.Stamp([]byte(` { }`), cred)); got != `{"project":"proj-\"9\""}` {
		t.Errorf("stamped empty = %s", got)
	}
	for _, body := range []string{`[1]`, ``} {
		if got := string(Provider{}.Stamp([]byte(body), cred)); got != body {
			t.Errorf("stamped %q into %q", body, got)
		}
	}
	if got := string(Provider{}.Stamp([]byte(`{"a":1}`), provider.Credential{})); got != `{"a":1}` {
		t.Errorf("stamped no project: %s", got)
	}
}

// stream is a Code Assist stream: data lines only, a blank line after each.
func stream(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("data: " + e + "\r\n\r\n")
	}
	return b.String()
}

func TestStreamMeterReadsTheLastUsage(t *testing.T) {
	body := stream(
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"thinking","thought":true}]}}],"usageMetadata":{"promptTokenCount":100,"thoughtsTokenCount":3}},"traceId":"t1"}`,
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"pong"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":60,"candidatesTokenCount":2,"thoughtsTokenCount":7,"totalTokenCount":109}},"traceId":"t1"}`,
	)
	var (
		u     provider.Usage
		first time.Time
		serr  string
	)
	m := Provider{}.Meter("text/event-stream", provider.Reading{Usage: &u, FirstContent: &first, StreamError: &serr})
	// Fed a byte at a time: a line may arrive in any number of pieces.
	for i := range len(body) {
		m.Feed([]byte{body[i]})
	}
	m.Done()
	want := provider.Usage{InputTokens: 40, CacheReadTokens: 60, OutputTokens: 9, ReasoningTokens: 7}
	if u != want {
		t.Errorf("usage = %+v, want %+v", u, want)
	}
	if first.IsZero() {
		t.Error("no first content")
	}
	if serr != "" {
		t.Errorf("stream error = %q", serr)
	}
}

func TestStreamMeterRecordsFailuresAfterTheStatus(t *testing.T) {
	for name, tc := range map[string]struct{ body, code string }{
		"error in place of an answer": {
			stream(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"quota","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`),
			"quota_exhausted",
		},
		"prompt blocked": {
			stream(`{"response":{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT"}}}`),
			"blocked",
		},
		"model declined": {
			stream(`{"response":{"candidates":[{"content":{"parts":[{"text":"I"}]}}]}}`, `{"response":{"candidates":[{"finishReason":"SAFETY"}]}}`),
			"safety",
		},
	} {
		var serr string
		m := Provider{}.Meter("text/event-stream", provider.Reading{StreamError: &serr})
		m.Feed([]byte(tc.body))
		m.Done()
		if got := ErrorCode(200, serr); got != tc.code {
			t.Errorf("%s: recorded %q, classified %q, want %q", name, serr, got, tc.code)
		}
	}
	// Running out of room is a finished answer, as Anthropic's max_tokens is.
	var serr string
	m := Provider{}.Meter("text/event-stream", provider.Reading{StreamError: &serr})
	m.Feed([]byte(stream(`{"response":{"candidates":[{"content":{"parts":[{"text":"x"}]},"finishReason":"MAX_TOKENS"}]}}`)))
	if serr != "" {
		t.Errorf("MAX_TOKENS recorded as %q", serr)
	}
}

func TestMeterReadsAWholeAnswerAndAnUntypedStream(t *testing.T) {
	var u provider.Usage
	m := Provider{}.Meter("application/json", provider.Reading{Usage: &u})
	m.Feed([]byte(`[{"response":{"usageMetadata":{"promptTokenCount":5}}},{"response":{"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":4}}}]`))
	m.Done()
	if u.InputTokens != 5 || u.OutputTokens != 4 {
		t.Errorf("array usage = %+v", u)
	}
	u = provider.Usage{}
	m = Provider{}.Meter("", provider.Reading{Usage: &u})
	m.Feed([]byte(stream(`{"response":{"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":1}}}`)))
	m.Done()
	if u.InputTokens != 8 || u.OutputTokens != 1 {
		t.Errorf("untyped stream usage = %+v", u)
	}
}

func rpcError(code int, status, message string, details ...string) []byte {
	return []byte(`{"error":{"code":` + itoa(code) + `,"status":"` + status + `","message":"` + message + `","details":[` + strings.Join(details, ",") + `]}}`)
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

const (
	quotaExhausted = `{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","domain":"cloudcode-pa.googleapis.com"}`
	retry90s       = `{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"90.5s"}`
	retry250ms     = `{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"250ms"}`
	capacity       = `{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"MODEL_CAPACITY_EXHAUSTED"}`
	validate       = `{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"VALIDATION_REQUIRED","domain":"cloudcode-pa.googleapis.com"}`
)

func TestVerdictReadsTheRPCDetails(t *testing.T) {
	v := Provider{}.Inspect(429, nil, rpcError(429, "RESOURCE_EXHAUSTED", "spent", quotaExhausted, retry90s))
	if d := time.Until(v.Until); d < 89*time.Second || d > 91*time.Second {
		t.Errorf("until in %v", d)
	}
	if v.Quota != nil || v.Kind != "" {
		t.Errorf("verdict = %+v", v)
	}
	if v := (Provider{}).Inspect(429, nil, rpcError(429, "RESOURCE_EXHAUSTED", "slow down", retry250ms)); time.Until(v.Until) > time.Second || v.Until.IsZero() {
		t.Errorf("ms delay until %v", v.Until)
	}
	if v := (Provider{}).Inspect(503, nil, rpcError(503, "UNAVAILABLE", "busy", capacity)); v.Kind != "server" {
		t.Errorf("capacity kind = %q", v.Kind)
	}
	if v := (Provider{}).Inspect(403, nil, rpcError(403, "PERMISSION_DENIED", "verify", validate)); v.Kind != "forbidden" {
		t.Errorf("validation kind = %q", v.Kind)
	}
	if v := (Provider{}).Inspect(429, nil, rpcError(429, "RESOURCE_EXHAUSTED", "Quota exceeded, limit: 0, model: gemini-3-pro")); v.Kind != "forbidden" {
		t.Errorf("zero limit kind = %q", v.Kind)
	}
	if v := (Provider{}).Inspect(429, nil, rpcError(429, "RESOURCE_EXHAUSTED", "limit: 0.5 per second")); v.Kind != "" {
		t.Errorf("a fractional limit read as none: %q", v.Kind)
	}
	if v := (Provider{}).Inspect(200, nil, nil); v != (provider.Verdict{}) {
		t.Errorf("a success said %+v", v)
	}
}

func TestErrorCode(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   string
	}{
		{200, "", ""},
		{429, string(rpcError(429, "RESOURCE_EXHAUSTED", "x", quotaExhausted)), "quota_exhausted"},
		{400, string(rpcError(400, "INVALID_ARGUMENT", "bad")), "invalid_argument"},
		{429, `[` + string(rpcError(429, "RESOURCE_EXHAUSTED", "x")) + `]`, "resource_exhausted"},
		{200, "safety: the model stopped: SAFETY", "safety"},
		{502, "<html>bad gateway</html>", "other"},
		{0, "dial tcp: refused", "no_answer"},
	} {
		if got := ErrorCode(tc.status, tc.body); got != tc.want {
			t.Errorf("ErrorCode(%d, %.40q) = %q, want %q", tc.status, tc.body, got, tc.want)
		}
	}
}

const quotaBody = `{"buckets":[
 {"modelId":"gemini-2.5-flash","tokenType":"REQUESTS","remainingFraction":0.75,"resetTime":"2026-10-24T07:00:00Z"},
 {"modelId":"gemini-3-pro-preview","tokenType":"REQUESTS","remainingFraction":0},
 {"modelId":"gemini-2.5-pro","tokenType":"REQUESTS","remainingFraction":1},
 {"modelId":"gemini-3.1-flash-lite","tokenType":"REQUESTS","remainingFraction":1},
 {"modelId":"gemini-3-pro","tokenType":"REQUESTS","remainingFraction":0.5},
 {"modelId":"gemini-3-flash","tokenType":"REQUESTS","remainingFraction":1},
 {"modelId":"gemini-2.5-flash","tokenType":"TOKENS","remainingFraction":0.9}
]}`

// The list the account measured was offered, as the gateway lists it:
// Antigravity's autocomplete models, a disabled one and Anthropic's left out,
// Pro before Flash before Flash-Lite, each newest first and the most
// thinking first.
func TestModelListReadsTheMeasuredList(t *testing.T) {
	out, err := Provider{}.ModelList(readFixture(t, "antigravity-models.json").Body)
	if err != nil {
		t.Fatal(err)
	}
	var l struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			Provider    string `json:"provider"`
			MaxInput    int    `json:"max_input_tokens"`
			MaxTokens   int    `json:"max_tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &l); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range l.Data {
		ids = append(ids, m.ID)
		if strings.Contains(m.ID, "_") || strings.HasPrefix(m.ID, "claude-") || m.ID == "gemini-4-argon-disabled" {
			t.Errorf("listed %s", m.ID)
		}
	}
	got := strings.Join(ids, " ")
	// The picker leads with Gemini 3.8 Flash, so Flash leads here; Pro,
	// which it does not show, comes after it.
	for _, want := range []string{
		"gemini-3.8-flash-high gemini-3.8-flash-medium gemini-3.8-flash-low",
		"gemini-2.5-pro gemini-3.5-flash-lite gemini-3.1-flash-lite",
		"gemini-3.5-flash-lite gemini-3.1-flash-lite",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("order lacks %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "gpt-oss-120b-medium") {
		t.Errorf("a non-Gemini model ranks last: %s", got)
	}
	first := l.Data[0]
	if first.ID != "gemini-3.8-flash-high" || first.DisplayName != "Gemini 3.8 Flash (High)" || first.Provider != ID ||
		first.MaxInput != 1048576 || first.MaxTokens != 65536 {
		t.Errorf("first = %+v", first)
	}
	method, path, body := Provider{}.ModelsRequest("limit=5", provider.Credential{AccountID: "proj-9"})
	if method != http.MethodPost || path != "/v1internal:fetchAvailableModels" || string(body) != `{"project":"proj-9"}` {
		t.Errorf("ModelsRequest = %s %s %s", method, path, body)
	}
}

func TestMeasuredQuotaIsPerModel(t *testing.T) {
	var q quotaResponse
	if err := json.Unmarshal(readFixture(t, "antigravity-quota.json").Body, &q); err != nil {
		t.Fatal(err)
	}
	rows := Accounts{}.Limits(q.quota(quotaRead).Detail)
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	// Measured: every Gemini model on one allowance, GPT-OSS on its own,
	// Claude's not shown.
	var titles []string
	for _, r := range rows {
		titles = append(titles, r.Title)
		if strings.Contains(r.Kind, "_") || strings.HasPrefix(r.Kind, "claude-") {
			t.Errorf("%s has a row", r.Kind)
		}
		// Recorded before any use: no allowance had a window yet, so none
		// has a reset to show, and each is a week long.
		if r.ResetsAt != "" || r.WindowSeconds != 7*24*60*60 {
			t.Errorf("%s: reset %q, window %d", r.Kind, r.ResetsAt, r.WindowSeconds)
		}
	}
	if len(rows) != 2 || !strings.HasPrefix(rows[0].Title, "Gemini — ") || !strings.HasPrefix(rows[1].Kind, "gpt-oss") {
		t.Errorf("rows = %q", titles)
	}
}

// The turn measured: usage normalised, the signature in its own empty part,
// nothing taken for a failure.
func TestMeasuredTurnIsMetered(t *testing.T) {
	f := readFixture(t, "antigravity-turn.json")
	var (
		u    provider.Usage
		serr string
	)
	m := Provider{}.Meter("text/event-stream", provider.Reading{Usage: &u, StreamError: &serr})
	m.Feed([]byte(f.streamBody(t)))
	m.Done()
	if u != (provider.Usage{InputTokens: 8, OutputTokens: 83, ReasoningTokens: 82}) || serr != "" {
		t.Errorf("usage = %+v, error %q", u, serr)
	}
}

// Without Antigravity's User-Agent the backend refuses, and says why.
func TestMeasuredRefusals(t *testing.T) {
	f := readFixture(t, "antigravity-turn-refused.json")
	if f.Status != 403 || ErrorCode(f.Status, string(f.Body)) != "subscription_required" {
		t.Errorf("refusal %d classified %q", f.Status, ErrorCode(f.Status, string(f.Body)))
	}
	var load loadResponse
	if err := json.Unmarshal(readFixture(t, "antigravity-load-refused.json").Body, &load); err != nil {
		t.Fatal(err)
	}
	if load.CurrentTier != nil || len(load.IneligibleTiers) == 0 || load.IneligibleTiers[0].ReasonCode != "UNSUPPORTED_CLIENT" {
		t.Errorf("refused load = %+v", load)
	}
	if err := json.Unmarshal(readFixture(t, "antigravity-load.json").Body, &load); err != nil {
		t.Fatal(err)
	}
	if load.CurrentTier == nil || load.CloudAICompanionProject != "aicode-consumers" {
		t.Errorf("served load = %+v", load)
	}
}

func TestAuthorizeSaysAntigravity(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, BaseURL+StreamPath, strings.NewReader("{}"))
	req.Header.Set("User-Agent", "claude-cli/2.1.274")
	Provider{}.Authorize(req, provider.Credential{Token: "ya29.x"})
	if req.Header.Get("User-Agent") != UserAgent || req.Header.Get("Authorization") != "Bearer ya29.x" {
		t.Errorf("headers = %v", req.Header)
	}
}

// A used allowance keeps its reset; one never used shows none, its reset
// being a week from whenever it was asked.
func TestAnUnusedAllowanceHasNoReset(t *testing.T) {
	used, unused := 0.96, 1.0
	q := quotaResponse{Buckets: []bucket{
		{ModelID: "gemini-3.8-flash-high", RemainingFraction: &used, ResetTime: "2026-10-30T09:46:47Z"},
		{ModelID: "gpt-oss-120b-medium", RemainingFraction: &unused, ResetTime: "2026-10-30T12:15:49Z"},
	}}
	rows := Accounts{}.Limits(q.quota(quotaRead).Detail)
	if len(rows) != 2 || rows[0].ResetsAt != "2026-10-30T09:46:47Z" || rows[1].ResetsAt != "" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestQuotaIsPerModel(t *testing.T) {
	var q quotaResponse
	if err := json.Unmarshal([]byte(quotaBody), &q); err != nil {
		t.Fatal(err)
	}
	got := q.quota(quotaRead)
	if got.Limited || len(got.Windows) != 0 {
		t.Errorf("quota = %+v", got)
	}
	rows := Accounts{}.Limits(got.Detail)
	// Three models untouched with no reset are one allowance's row.
	if len(rows) != 5 {
		t.Fatalf("%d rows: %+v", len(rows), rows)
	}
	folded := false
	for _, r := range rows {
		folded = folded || r.Title == "Gemini — 3 models"
	}
	if !folded {
		t.Errorf("no folded row: %+v", rows)
	}
	var spent provider.Limit
	for _, r := range rows {
		if r.Kind == "gemini-3-pro-preview" {
			spent = r
		}
	}
	if spent.Percent != 100 || spent.Severity != "rejected" || !spent.IsActive {
		t.Errorf("spent row = %+v", spent)
	}
	q.Buckets = []bucket{{ModelID: "gemini-2.5-pro", RemainingFraction: new(float64)}}
	if !q.quota(quotaRead).Limited {
		t.Error("an account with every model spent was not limited")
	}
}

// quotaRead is when antigravity-quota.json was recorded: its unused
// allowances reset a week later.
var quotaRead = time.Date(2026, 10, 23, 9, 46, 46, 0, time.UTC)

// Measured on Google AI Pro, all unused: five hours for every allowance but
// Gemini 4 Argon's, which is a week; so Argon is a row of its own, named by
// what its ids share, and each row has its own length.
func TestProAllowancesHaveTheirOwnLengths(t *testing.T) {
	var q quotaResponse
	if err := json.Unmarshal(readFixture(t, "antigravity-quota-pro.json").Body, &q); err != nil {
		t.Fatal(err)
	}
	rows := Accounts{}.Limits(q.quota(time.Date(2026, 10, 23, 13, 17, 34, 0, time.UTC)).Detail)
	got := map[string]int64{}
	for _, r := range rows {
		got[r.Title] = r.WindowSeconds
	}
	want := map[string]int64{
		"Gemini — 15 models":        5 * 60 * 60,
		"Gemini 4 Argon — 3 models": 7 * 24 * 60 * 60,
		"GPT OSS 120b Medium":       5 * 60 * 60,
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %v", got)
	}
	for title, w := range want {
		if got[title] != w {
			t.Errorf("%s: window %d, want %d (rows %v)", title, got[title], w, got)
		}
	}
}

// The families' order is the picker's, whatever their words, then the sizes
// the picker does not show; a family's levels keep the most thinking first.
func TestModelsFollowThePickersFamilies(t *testing.T) {
	body := `{"agentModelSorts":[{"groups":[{"modelIds":["gemini-4-argon-medium","gemini-4-argon-high","gemini-3.8-flash-high"]}]}],
	  "models":{
	    "gemini-2.5-pro":{"displayName":"Gemini 2.5 Pro","quotaInfo":{}},
	    "gemini-3.5-flash-lite":{"displayName":"Gemini 3.5 Flash Lite","quotaInfo":{}},
	    "gemini-3.8-flash-high":{"displayName":"Gemini 3.8 Flash (High)","quotaInfo":{}},
	    "gemini-4-argon-low":{"displayName":"Gemini 4 Argon (Low)","quotaInfo":{}},
	    "gemini-4-argon-high":{"displayName":"Gemini 4 Argon (High)","quotaInfo":{}},
	    "gemini-4-argon-medium":{"displayName":"Gemini 4 Argon (Medium)","quotaInfo":{}}}}`
	raw, err := Provider{}.ModelList([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var l struct {
		Data []struct{ ID string } `json:"data"`
	}
	if err := json.Unmarshal(raw, &l); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range l.Data {
		ids = append(ids, m.ID)
	}
	want := []string{"gemini-4-argon-high", "gemini-4-argon-medium", "gemini-4-argon-low",
		"gemini-3.8-flash-high", "gemini-2.5-pro", "gemini-3.5-flash-lite"}
	if strings.Join(ids, " ") != strings.Join(want, " ") {
		t.Errorf("order = %v\nwant    %v", ids, want)
	}
}
