package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
// to a fake below, through a client whose transport is the fake.
func TestMain(m *testing.M) { testenv.Offline(m) }

// probeFile is one of scripts/probe-chatgpt.py's records, scrubbed, as kept in
// testdata.
type probeFile struct {
	Status     int               `json:"status"`
	Headers    map[string]string `json:"headers"`
	EventNames []string          `json:"event_names"`
	Usage      json.RawMessage   `json:"usage"`
	Model      string            `json:"model"`
	ErrorBody  *string           `json:"error_body"`
	Body       json.RawMessage   `json:"body"`
}

func fixture(t *testing.T, name string) probeFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var f probeFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return f
}

func (f probeFile) header() http.Header {
	h := http.Header{}
	for k, v := range f.Headers {
		h.Set(k, v)
	}
	return h
}

// stream is a Responses stream with the fixture's events in order, the last
// carrying its usage, as the backend sends them.
func (f probeFile) stream() string {
	// One line per data field: the fixture is indented.
	var usage bytes.Buffer
	_ = json.Compact(&usage, f.Usage)
	var b strings.Builder
	for _, name := range f.EventNames {
		data := `{"type":"` + name + `"}`
		switch name {
		case evCompleted:
			data = `{"type":"response.completed","response":{"id":"resp_1","model":"` + f.Model + `","usage":` + usage.String() + `}}`
		case "response.output_text.delta":
			data = `{"type":"response.output_text.delta","delta":"ok"}`
		}
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", name, data)
	}
	return b.String()
}

// roundTrip lets a handler stand in for every host the package calls.
type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func clientFor(h http.Handler) *http.Client {
	return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Result(), nil
	})}
}

// jwt is an unsigned token with the given payload, which is all the gateway
// reads of one.
func jwt(payload map[string]any) string {
	raw, _ := json.Marshal(payload)
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(raw) + ".sig"
}

// --- wire -------------------------------------------------------------------

// The credential and the workspace go on; the caller's originator stays its
// own, and only a caller that sent none is given the gateway's name — never
// Codex's.
func TestAuthorizeAddsOnlyTheCredential(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, BaseURL+"/responses", nil)
	req.Header.Set("Originator", "codex_cli_rs")
	req.Header.Set("Session-Id", "s-1")
	Provider{}.Authorize(req, provider.Credential{Token: "tok", AccountID: "acct-1"})
	if got := req.Header.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization = %q", got)
	}
	if got := req.Header.Get("ChatGPT-Account-ID"); got != "acct-1" {
		t.Errorf("ChatGPT-Account-ID = %q", got)
	}
	if got := req.Header.Get("Originator"); got != "codex_cli_rs" {
		t.Errorf("the caller's originator became %q", got)
	}
	if got := req.Header.Get("Session-Id"); got != "s-1" {
		t.Errorf("session-id = %q", got)
	}

	bare := httptest.NewRequest(http.MethodPost, BaseURL+"/responses", nil)
	Provider{}.Authorize(bare, provider.Credential{Token: "tok"})
	if got := bare.Header.Get("Originator"); got != Originator {
		t.Errorf("no originator sent: got %q, want %q", got, Originator)
	}
}

func TestEventKinds(t *testing.T) {
	for name, want := range map[string]provider.EventKind{
		"response.created":                      provider.EventQuiet,
		"response.in_progress":                  provider.EventQuiet,
		"response.output_item.added":            provider.EventQuiet,
		"response.metadata":                     provider.EventQuiet,
		"response.output_text.delta":            provider.EventContent,
		"response.reasoning_summary_text.delta": provider.EventContent,
		"response.output_item.done":             provider.EventSettling,
		"response.completed":                    provider.EventSettling,
		"response.failed":                       provider.EventError,
		"error":                                 provider.EventError,
	} {
		if got := (Provider{}).Event([]byte(name)); got != want {
			t.Errorf("%s = %d, want %d", name, got, want)
		}
	}
}

// The measured stream, fed in awkward pieces, yields its usage in the
// gateway's vocabulary, and no error.
func TestMeterReadsTheMeasuredStream(t *testing.T) {
	f := fixture(t, "reasoning.json")
	var (
		u     provider.Usage
		err   string
		first time.Time
	)
	m := Provider{}.Meter("text/event-stream; charset=utf-8", provider.Reading{Usage: &u, StreamError: &err, FirstContent: &first})
	s := f.stream()
	for len(s) > 0 {
		n := min(7, len(s))
		m.Feed([]byte(s[:n]))
		s = s[n:]
	}
	m.Done()
	if u.InputTokens != 53 || u.OutputTokens != 18 || u.ReasoningTokens != 9 || u.CacheReadTokens != 0 {
		t.Errorf("usage = %+v, want 53 in, 18 out of which 9 reasoning", u)
	}
	if err != "" {
		t.Errorf("stream error %q on a stream that completed", err)
	}
	if first.IsZero() {
		t.Error("no time to first token")
	}
}

// Cached input is inside OpenAI's input_tokens and outside Anthropic's; left
// as it came, a cached turn would count twice in every budget.
func TestMeterTakesCachedInputOutOfInput(t *testing.T) {
	var u provider.Usage
	m := Provider{}.Meter("text/event-stream", provider.Reading{Usage: &u})
	m.Feed([]byte("event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":1000," +
		"\"input_tokens_details\":{\"cached_tokens\":800,\"cache_write_tokens\":50},\"output_tokens\":20," +
		"\"output_tokens_details\":{\"reasoning_tokens\":5}}}}\n\n"))
	if u.InputTokens != 200 || u.CacheReadTokens != 800 || u.CacheCreationTokens != 50 ||
		u.OutputTokens != 20 || u.ReasoningTokens != 5 {
		t.Errorf("usage = %+v", u)
	}

	// A non-streamed answer carries the same object at the top level.
	var j provider.Usage
	jm := Provider{}.Meter("application/json", provider.Reading{Usage: &j})
	jm.Feed([]byte(`{"id":"resp_1","usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":4},"output_tokens":3}}`))
	jm.Done()
	if j.InputTokens != 6 || j.CacheReadTokens != 4 || j.OutputTokens != 3 {
		t.Errorf("json usage = %+v", j)
	}
}

// A stream that fails after its 200 is recorded as failing, in the
// "code: message" form ErrorCode reads back.
func TestMeterRecordsAFailureInsideTheStream(t *testing.T) {
	for stream, want := range map[string]string{
		"event: response.failed\ndata: {\"response\":{\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"too long\"}}}\n\n": "context_length_exceeded: too long",
		"event: error\ndata: {\"type\":\"error\",\"code\":\"rate_limit_exceeded\",\"message\":\"try again in 3s\"}\n\n":                "rate_limit_exceeded: try again in 3s",
		"event: response.incomplete\ndata: {\"response\":{\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n":             "incomplete: the response ended early: max_output_tokens",
	} {
		var got string
		m := Provider{}.Meter("text/event-stream", provider.Reading{StreamError: &got})
		m.Feed([]byte(stream))
		if got != want {
			t.Errorf("stream error = %q, want %q", got, want)
		}
		if code := ErrorCode(200, got); code != strings.SplitN(want, ":", 2)[0] {
			t.Errorf("ErrorCode(%q) = %q", got, code)
		}
	}
}

// The headers on the measured answer are the account's two windows, by length.
func TestInspectReadsTheWindowsOffTheHeaders(t *testing.T) {
	f := fixture(t, "baseline.json")
	v := Provider{}.Inspect(f.Status, f.header(), nil)
	if v.Quota == nil || len(v.Quota.Windows) != 2 {
		t.Fatalf("quota = %+v", v.Quota)
	}
	five, week := v.Quota.Windows[0], v.Quota.Windows[1]
	if five.Seconds != 18000 || week.Seconds != 604800 {
		t.Errorf("windows %d s and %d s, want 5 h and 7 d", five.Seconds, week.Seconds)
	}
	if !five.ResetAt.Equal(time.Unix(1792446828, 0)) {
		t.Errorf("5-hour reset = %s", five.ResetAt)
	}
	if v.Quota.Limited || !v.Until.IsZero() || v.Kind != "" {
		t.Errorf("a 200 said more than its windows: %+v", v)
	}
	rows := limitRows(v.Quota.Detail)
	if len(rows) != 2 || rows[0].Title != "5-hour limit" || rows[1].Title != "Weekly limit" {
		t.Errorf("rows = %+v", rows)
	}

	// An answer with none of these headers is no quota at all, not an idle
	// account.
	if v := (Provider{}).Inspect(200, http.Header{}, nil); v.Quota != nil {
		t.Errorf("no headers read as %+v", v.Quota)
	}
}

// The plan's own limit carries its reset, which the pool sits the account out
// until; a plan without Codex is not a rate limit at all.
func TestInspectReadsTheRefusal(t *testing.T) {
	limit := []byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached",` +
		`"plan_type":"plus","resets_at":1792446828,"limit_window_minutes":300}}`)
	// Without the windows' headers: the reset alone, and no quota to write
	// over the figures already known.
	v := Provider{}.Inspect(http.StatusTooManyRequests, http.Header{}, limit)
	if !v.Until.Equal(time.Unix(1792446828, 0)) || v.Quota != nil {
		t.Errorf("usage_limit_reached without headers = %+v", v)
	}
	// With them: the windows, closed.
	v = Provider{}.Inspect(http.StatusTooManyRequests, fixture(t, "baseline.json").header(), limit)
	if v.Quota == nil || len(v.Quota.Windows) != 2 || !v.Quota.Limited {
		t.Errorf("usage_limit_reached with headers = %+v", v.Quota)
	}
	if ErrorCode(429, string(limit)) != "usage_limit_reached" {
		t.Errorf("ErrorCode = %q", ErrorCode(429, string(limit)))
	}

	v = Provider{}.Inspect(http.StatusTooManyRequests, http.Header{},
		[]byte(`{"error":{"type":"usage_not_included","message":"upgrade"}}`))
	if v.Kind != "forbidden" || !v.Until.IsZero() {
		t.Errorf("usage_not_included = %+v", v)
	}
}

// The measured surrogate refusal names nothing, and is filed as what it is.
func TestErrorCodes(t *testing.T) {
	f := fixture(t, "surrogate.json")
	if got := ErrorCode(f.Status, *f.ErrorBody); got != "bad_request" {
		t.Errorf("surrogate refusal = %q", got)
	}
	for body, want := range map[string]string{
		`{"error":{"code":"server_is_overloaded","message":"busy"}}`: "server_is_overloaded",
		`{"error":{"type":"error","code":"cyber_policy"}}`:           "cyber_policy",
		"":                         "",
		"<html>bad gateway</html>": "other",
	} {
		if got := ErrorCode(500, body); got != want {
			t.Errorf("ErrorCode(%q) = %q, want %q", body, got, want)
		}
	}
	if ErrorCode(0, "dial tcp: refused") != "no_answer" {
		t.Error("a failure with no status is no_answer")
	}
}

// The measured list: seven listed, two hidden left out, in the backend's own
// ranking, without the instructions each entry carries.
func TestModelListKeepsTheListedOnesInRank(t *testing.T) {
	f := fixture(t, "models.json")
	out, err := Provider{}.ModelList(f.Body)
	if err != nil {
		t.Fatal(err)
	}
	var l struct {
		Data []struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
			MaxInput int    `json:"max_input_tokens"`
			Priority int    `json:"priority"`
		} `json:"data"`
		FirstID string `json:"first_id"`
	}
	if err := json.Unmarshal(out, &l); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range l.Data {
		ids = append(ids, m.ID)
		if m.Provider != ID {
			t.Errorf("%s provider = %q", m.ID, m.Provider)
		}
	}
	want := "gpt-6.1-sol gpt-6-astra gpt-6-sol gpt-6-luna gpt-5.6-sol gpt-5.6-terra gpt-5.6-luna"
	if got := strings.Join(ids, " "); got != want {
		t.Errorf("listed %s\n want %s", got, want)
	}
	if l.Data[0].MaxInput != 500000 || l.FirstID != "gpt-6.1-sol" {
		t.Errorf("first = %+v, first_id %q", l.Data[0], l.FirstID)
	}
	if strings.Contains(string(out), "base_instructions") {
		t.Error("the instructions came through")
	}
	if got := (Provider{}).ModelsPath("limit=1000"); got != "/models?client_version=999.0.0" {
		t.Errorf("ModelsPath = %q", got)
	}
}

// --- accounts ---------------------------------------------------------------

// The measured /wham/usage answer is the same two windows, and a closed plan
// takes the account out.
func TestUsageReadsTheMeasuredAnswer(t *testing.T) {
	f := fixture(t, "usage.json")
	var seen http.Header
	c := clientFor(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != "chatgpt.com" || r.URL.Path != "/backend-api/wham/usage" {
			http.NotFound(w, r)
			return
		}
		seen = r.Header.Clone()
		_, _ = w.Write(f.Body)
	}))
	q, err := FetchUsage(context.Background(), c, provider.Credential{Token: "tok", AccountID: "acct-1"})
	if err != nil {
		t.Fatal(err)
	}
	if seen.Get("Authorization") != "Bearer tok" || seen.Get("ChatGPT-Account-Id") != "acct-1" {
		t.Errorf("asked with %v", seen)
	}
	if len(q.Windows) != 2 || q.Windows[0].Seconds != 18000 || q.Windows[1].Seconds != 604800 || q.Limited {
		t.Errorf("quota = %+v", q)
	}

	closed := usageResponse{}
	_ = json.Unmarshal([]byte(`{"rate_limit":{"allowed":false,"limit_reached":true,
		"primary_window":{"used_percent":100,"limit_window_seconds":18000,"reset_at":1792446828}}}`), &closed)
	if q := closed.quota(); !q.Limited || q.Windows[0].Status != "limit_reached" {
		t.Errorf("closed plan = %+v", q)
	}
}

// The identity comes from the id_token: nested account id and plan as
// measured, top-level account id as crush also accepts, and the access token
// when there is no id_token.
func TestClaims(t *testing.T) {
	nested := jwt(map[string]any{"email": "a@example.com", "https://api.openai.com/auth": map[string]any{
		"chatgpt_account_id": "acct-nested", "chatgpt_plan_type": "plus"}})
	if c := claimsOf(nested); c.email() != "a@example.com" || c.accountID() != "acct-nested" || c.plan() != "plus" {
		t.Errorf("nested = %+v", c)
	}
	top := jwt(map[string]any{"chatgpt_account_id": "acct-top",
		"https://api.openai.com/profile": map[string]any{"email": "b@example.com"}})
	if c := claimsOf(top); c.accountID() != "acct-top" || c.email() != "b@example.com" {
		t.Errorf("top-level = %+v", c)
	}
	if c := claimsOf("not-a-jwt"); c.accountID() != "" {
		t.Errorf("garbage = %+v", c)
	}
}

// fakeAuth is auth.openai.com: the device-code endpoints and the token
// endpoint, recording what was sent.
type fakeAuth struct {
	mu        sync.Mutex
	polls     int
	approveAt int
	forms     []url.Values
	jsons     []map[string]string
	refresh   func(map[string]string) (int, string)
}

func (f *fakeAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	if r.URL.Host != "auth.openai.com" {
		http.NotFound(w, r)
		return
	}
	switch r.URL.Path {
	case "/api/accounts/deviceauth/usercode":
		_, _ = io.WriteString(w, `{"device_auth_id":"dev-1","user_code":"ABCD-1234","interval":"1"}`)
	case "/api/accounts/deviceauth/token":
		f.polls++
		if f.polls < f.approveAt {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":"authorization_pending"}`)
			return
		}
		_, _ = io.WriteString(w, `{"authorization_code":"code-1","code_verifier":"verifier-1"}`)
	case "/oauth/token":
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			var m map[string]string
			_ = json.Unmarshal(body, &m)
			f.jsons = append(f.jsons, m)
			status, out := f.refresh(m)
			w.WriteHeader(status)
			_, _ = io.WriteString(w, out)
			return
		}
		form, _ := url.ParseQuery(string(body))
		f.forms = append(f.forms, form)
		id := jwt(map[string]any{"email": "dev@example.com", "https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-dev", "chatgpt_plan_type": "pro"}})
		_, _ = fmt.Fprintf(w, `{"access_token":"access-1","refresh_token":"refresh-1","id_token":%q,"expires_in":3600}`, id)
	default:
		http.NotFound(w, r)
	}
}

// The device flow, as Codex runs it: pending twice, then approved, then the
// ordinary exchange with the device redirect.
func TestDeviceFlow(t *testing.T) {
	auth := &fakeAuth{approveAt: 3}
	c := clientFor(auth)
	d, err := StartDevice(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if d.UserCode != "ABCD-1234" || d.VerificationURI != "https://auth.openai.com/codex/device" ||
		d.Interval != time.Second || time.Until(d.ExpiresAt) < 14*time.Minute {
		t.Errorf("device = %+v", d)
	}
	for i := 0; i < 2; i++ {
		if _, pending, err := PollDevice(context.Background(), c, d); !pending || err != nil {
			t.Fatalf("poll %d: pending %v, %v; want pending", i, pending, err)
		}
	}
	res, pending, err := PollDevice(context.Background(), c, d)
	if pending || err != nil {
		t.Fatalf("approved poll: pending %v, %v", pending, err)
	}
	if res.Email != "dev@example.com" || res.AccountUUID != "acct-dev" || res.Plan != "pro" ||
		res.AccessToken != "access-1" || res.RefreshToken != "refresh-1" {
		t.Errorf("result = %+v", res)
	}
	form := auth.forms[0]
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != "code-1" ||
		form.Get("code_verifier") != "verifier-1" || form.Get("client_id") != ClientID ||
		form.Get("redirect_uri") != "https://auth.openai.com/deviceauth/callback" {
		t.Errorf("exchange form = %v", form)
	}

	expired := d
	expired.ExpiresAt = time.Now().Add(-time.Second)
	if _, _, err := PollDevice(context.Background(), c, expired); !errors.Is(err, oauth.ErrDeviceSpent) {
		t.Error("an expired code was polled")
	}
}

// Refresh sends JSON as Codex does, keeps the old refresh token when none
// comes back, and reads a refusal that will never come right as such.
func TestRefresh(t *testing.T) {
	auth := &fakeAuth{refresh: func(m map[string]string) (int, string) {
		if m["refresh_token"] == "dead" {
			return http.StatusBadRequest, `{"error":{"code":"refresh_token_reused"}}`
		}
		return http.StatusOK, `{"access_token":"new-access","expires_in":3600}`
	}}
	c := clientFor(auth)
	res, err := Refresh(context.Background(), c, "old-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if res.AccessToken != "new-access" || res.RefreshToken != "old-refresh" {
		t.Errorf("refresh = %+v", res)
	}
	if m := auth.jsons[0]; m["grant_type"] != "refresh_token" || m["client_id"] != ClientID {
		t.Errorf("sent %v", m)
	}
	if _, err := Refresh(context.Background(), c, "dead"); !errors.Is(err, oauth.ErrInvalidGrant) {
		t.Errorf("reused token = %v, want ErrInvalidGrant", err)
	}
}

// A probe is one short turn on the account's own first model, read as Codex
// reads it.
func TestProbe(t *testing.T) {
	models := fixture(t, "models.json")
	turn := fixture(t, "baseline.json")
	var sent map[string]any
	c := clientFor(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/codex/models":
			_, _ = w.Write(models.Body)
		case "/backend-api/codex/responses":
			if r.Header.Get("ChatGPT-Account-Id") != "acct-1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&sent)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, turn.stream())
		default:
			http.NotFound(w, r)
		}
	}))
	res := Probe(context.Background(), c, provider.Credential{Token: "tok", AccountID: "acct-1"}, "")
	if !res.OK || res.Reply != "ok" || res.Model != "gpt-6.1-sol" || res.InputTokens != 23 || res.OutputTokens != 5 {
		t.Errorf("probe = %+v", res)
	}
	if sent["model"] != "gpt-6.1-sol" || sent["stream"] != true || sent["store"] != false {
		t.Errorf("sent %v", sent)
	}

	refused := clientFor(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"type":"usage_not_included"}}`)
	}))
	if res := Probe(context.Background(), refused, provider.Credential{Token: "tok"}, "gpt-6-luna"); res.OK ||
		!strings.Contains(res.Error, "does not include Codex") {
		t.Errorf("plan without Codex = %+v", res)
	}
}

func TestWindowLabels(t *testing.T) {
	for secs, want := range map[int64]string{
		18000: "5-hour limit", 604800: "Weekly limit", 86400: "1-day limit", 900: "15-minute limit", 0: "Limit",
	} {
		if got := WindowLabel(secs); got != want {
			t.Errorf("WindowLabel(%d) = %q, want %q", secs, got, want)
		}
	}
}

// A response.completed line longer than anything held — a turn that wrote a
// large file repeats it there — still gives up its usage, wherever in the
// line the usage object sits, and fed in pieces that split its key.
func TestMeterFindsTheUsageInAHugeLine(t *testing.T) {
	usage := `"usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":600},` +
		`"output_tokens":4000,"output_tokens_details":{"reasoning_tokens":7}}`
	huge := `"output":[{"type":"message","content":[{"type":"output_text","text":"` +
		strings.Repeat("x", 9<<20) + `"}]}]`
	for name, payload := range map[string]string{
		"usage last":  `{"response":{"id":"r",` + huge + `,` + usage + `}}`,
		"usage first": `{"response":{"id":"r",` + usage + `,` + huge + `}}`,
	} {
		var u provider.Usage
		var streamErr string
		m := Provider{}.Meter("text/event-stream", provider.Reading{Usage: &u, StreamError: &streamErr})
		stream := "event: response.completed\ndata: " + payload + "\n\n"
		for len(stream) > 0 {
			n := min(65531, len(stream)) // odd, so the key is split somewhere
			m.Feed([]byte(stream[:n]))
			stream = stream[n:]
		}
		m.Done()
		if u.InputTokens != 400 || u.CacheReadTokens != 600 || u.OutputTokens != 4000 || u.ReasoningTokens != 7 {
			t.Errorf("%s: usage = %+v", name, u)
		}
		if streamErr != "" {
			t.Errorf("%s: stream error %q", name, streamErr)
		}
	}
}

// A real streamed answer, as the backend sent it on 2026-10-20 through the
// gateway (testdata/live-stream.txt, the safety identifier scrubbed): no
// Content-Type at all, and response.created carrying tool_usage and the rest
// of what the fixtures did not. Read by its header it was JSON, and recorded
// as no tokens; read by its first byte it is the stream it is.
func TestMeterReadsAStreamThatNamesNoType(t *testing.T) {
	raw, err := os.ReadFile("testdata/live-stream.txt")
	if err != nil {
		t.Fatal(err)
	}
	var (
		u         provider.Usage
		streamErr string
		first     time.Time
	)
	m := Provider{}.Meter("", provider.Reading{Usage: &u, StreamError: &streamErr, FirstContent: &first})
	for len(raw) > 0 {
		n := min(512, len(raw))
		m.Feed(raw[:n])
		raw = raw[n:]
	}
	m.Done()
	if u.InputTokens != 21 || u.OutputTokens != 5 || streamErr != "" || first.IsZero() {
		t.Errorf("usage = %+v, stream error %q, first content %v", u, streamErr, first)
	}

	// And an answer naming no type that is JSON is read as JSON.
	var j provider.Usage
	jm := Provider{}.Meter("", provider.Reading{Usage: &j})
	jm.Feed([]byte(`  {"usage":{"input_tokens":4,"output_tokens":2}}`))
	jm.Done()
	if j.InputTokens != 4 || j.OutputTokens != 2 {
		t.Errorf("json usage = %+v", j)
	}
}
