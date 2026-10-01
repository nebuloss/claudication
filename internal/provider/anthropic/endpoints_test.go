package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"claudication/internal/oauth"
	"claudication/internal/provider"
)

// seen is one request as the fake upstream received it, including the host
// the caller aimed at before the transport redirected it.
type seen struct {
	method, host, path, rawQuery string
	header                       http.Header
	body                         []byte
}

// redirect is a RoundTripper that sends every request to a test server while
// remembering where it was meant to go. The package's endpoints are fixed
// constants, and the host each one names is itself part of what was reversed
// out of the client — so tests check the intended host rather than replace it.
type redirect struct {
	target *url.URL
	next   http.RoundTripper

	mu   sync.Mutex
	reqs []seen
}

func (r *redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		req.Body.Close()
	}
	r.mu.Lock()
	r.reqs = append(r.reqs, seen{
		method: req.Method, host: req.URL.Host, path: req.URL.Path,
		rawQuery: req.URL.RawQuery, header: req.Header.Clone(), body: body,
	})
	r.mu.Unlock()

	out := req.Clone(req.Context())
	out.URL.Scheme = r.target.Scheme
	out.URL.Host = r.target.Host
	out.Host = r.target.Host
	out.Body = io.NopCloser(strings.NewReader(string(body)))
	out.ContentLength = int64(len(body))
	return r.next.RoundTrip(out)
}

func (r *redirect) last(t *testing.T) seen {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reqs) == 0 {
		t.Fatal("no request reached the upstream")
	}
	return r.reqs[len(r.reqs)-1]
}

// upstream starts a test server with the given handler and returns a client
// whose every request lands on it.
func upstream(t *testing.T, h http.HandlerFunc) (*http.Client, *redirect) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	rt := &redirect{target: u, next: srv.Client().Transport}
	return &http.Client{Transport: rt}, rt
}

// --- token endpoint ---------------------------------------------------------

// The exchange must carry exactly the fields the client sends, to the host
// that was shown to parse them; anything else is rejected with an error that
// names a field the request plainly contained.
func TestExchangeCodeSendsTheClientsRequest(t *testing.T) {
	client, rt := upstream(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"at","refresh_token":"rt","expires_in":7200,
			"refresh_token_expires_in":86400,
			"account":{"email_address":"a@example.com","uuid":"u-1"}}`)
	})

	before := time.Now()
	res, err := ExchangeCode(context.Background(), client, "the-code", "verifier", "st", RedirectManual)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}

	got := rt.last(t)
	if got.method != http.MethodPost || got.host != "api.anthropic.com" || got.path != "/v1/oauth/token" {
		t.Errorf("sent %s %s%s, want POST api.anthropic.com/v1/oauth/token", got.method, got.host, got.path)
	}
	if ct := got.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var fields map[string]string
	if err := json.Unmarshal(got.body, &fields); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	want := map[string]string{
		"code": "the-code", "grant_type": "authorization_code", "client_id": ClientID,
		"redirect_uri": RedirectManual, "code_verifier": "verifier", "state": "st",
	}
	for k, v := range want {
		if fields[k] != v {
			t.Errorf("%s = %q, want %q", k, fields[k], v)
		}
	}

	if res.AccessToken != "at" || res.RefreshToken != "rt" || res.Email != "a@example.com" || res.AccountUUID != "u-1" {
		t.Errorf("result: %+v", res)
	}
	if d := res.ExpiresAt.Sub(before); d < 2*time.Hour-time.Minute || d > 2*time.Hour+time.Minute {
		t.Errorf("access token expires in %s, want about 2h", d)
	}
	// The refresh token's own lifetime is what warns an operator before an
	// account lapses; losing it means the account dies silently.
	if d := res.RefreshTokenExpiresAt.Sub(before); d < 24*time.Hour-time.Minute || d > 24*time.Hour+time.Minute {
		t.Errorf("refresh token expires in %s, want about 24h", d)
	}
}

// Some refreshes return no new refresh token. Storing the empty string would
// lock the account out on the next refresh, so the old one is kept.
func TestRefreshKeepsTheOldRefreshTokenWhenNoneIsReturned(t *testing.T) {
	client, rt := upstream(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"new-at"}`)
	})

	before := time.Now()
	res, err := Refresh(context.Background(), client, "old-rt")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if res.RefreshToken != "old-rt" {
		t.Errorf("refresh token = %q, want the old one kept", res.RefreshToken)
	}
	// No expires_in means the provider did not say; an hour is the floor
	// rather than an access token that is already expired.
	if d := res.ExpiresAt.Sub(before); d < 59*time.Minute || d > 61*time.Minute {
		t.Errorf("default expiry %s, want about 1h", d)
	}
	// Absent is not zero: no refresh-token deadline must stay unknown.
	if !res.RefreshTokenExpiresAt.IsZero() {
		t.Errorf("refresh token expiry invented: %s", res.RefreshTokenExpiresAt)
	}

	var fields map[string]string
	_ = json.Unmarshal(rt.last(t).body, &fields)
	if fields["grant_type"] != "refresh_token" || fields["refresh_token"] != "old-rt" || fields["client_id"] != ClientID {
		t.Errorf("refresh body: %v", fields)
	}
}

// A rotated refresh token must replace the old one, or the next refresh
// presents a spent token and the account is lost.
func TestRefreshTakesARotatedRefreshToken(t *testing.T) {
	client, _ := upstream(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"at","refresh_token":"rotated"}`)
	})
	res, err := Refresh(context.Background(), client, "old-rt")
	if err != nil {
		t.Fatal(err)
	}
	if res.RefreshToken != "rotated" {
		t.Errorf("refresh token = %q, want the rotated one", res.RefreshToken)
	}
}

// invalid_grant is the refusal no retry can fix, and callers stop presenting
// the token only if they can recognise it.
func TestRefreshMarksInvalidGrant(t *testing.T) {
	client, _ := upstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("request-id", "req_123")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"Refresh token revoked"}`)
	})
	_, err := Refresh(context.Background(), client, "spent")
	if !errors.Is(err, oauth.ErrInvalidGrant) {
		t.Fatalf("err = %v, want ErrInvalidGrant", err)
	}
	// The upstream's words and the request id are what an operator takes to
	// Anthropic; they must survive into the error.
	for _, want := range []string{"Refresh token revoked", "req_123", "400"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not carry %q", err, want)
		}
	}
}

// Any other refusal is passed through verbatim and is not mistaken for a dead
// token, which would stop an account being retried after a passing outage.
func TestTokenRefusalOtherThanInvalidGrant(t *testing.T) {
	client, _ := upstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"rate_limited"}`)
	})
	_, err := Refresh(context.Background(), client, "rt")
	if err == nil || errors.Is(err, oauth.ErrInvalidGrant) {
		t.Fatalf("err = %v, want a plain refusal", err)
	}
	if !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "rate_limited") {
		t.Errorf("refusal lost the upstream's words: %v", err)
	}
	if strings.Contains(err.Error(), "request-id") {
		t.Errorf("mentions a request id that was never sent: %v", err)
	}
}

// Following a redirect turns the POST into a bodiless GET, which the endpoint
// rejects naming a field the request did contain. Refusing to follow makes
// that failure say what it is.
func TestTokenExchangeRefusesToFollowARedirect(t *testing.T) {
	var hits int
	var mu sync.Mutex
	client, _ := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		http.Redirect(w, r, "https://elsewhere.example/token", http.StatusFound)
	})
	_, err := ExchangeCode(context.Background(), client, "c", "v", "s", RedirectManual)
	if err == nil || !strings.Contains(err.Error(), "redirected (302)") ||
		!strings.Contains(err.Error(), "elsewhere.example") {
		t.Fatalf("err = %v, want a redirect refusal naming the target", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Errorf("upstream saw %d requests, want 1: the redirect was followed", hits)
	}
}

// A 200 with nothing usable in it must be an error, not an account stored
// with an empty credential.
func TestTokenResponseWithoutAnAccessToken(t *testing.T) {
	for name, body := range map[string]string{
		"no access token": `{"refresh_token":"rt"}`,
		"not json":        `<html>maintenance</html>`,
	} {
		client, _ := upstream(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, body)
		})
		if res, err := Refresh(context.Background(), client, "rt"); err == nil {
			t.Errorf("%s: accepted %+v", name, res)
		}
	}
}

// A network failure must surface as an error naming the endpoint, not a
// zero-value result the caller would store.
func TestTokenEndpointUnreachable(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}
	_, err := ExchangeCode(context.Background(), client, "c", "v", "s", RedirectManual)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// --- revoke -----------------------------------------------------------------

// Revocation goes to platform.claude.com, where the client sends it, and not
// to the exchange host. The two are allowed to differ and must not be merged.
func TestRevokeGoesWhereTheClientSendsIt(t *testing.T) {
	client, rt := upstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if err := Revoke(context.Background(), client, "rt", ""); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	got := rt.last(t)
	if got.host != "platform.claude.com" || got.path != "/v1/oauth/token/revoke" {
		t.Errorf("revoked at %s%s", got.host, got.path)
	}
}

// --- probe ------------------------------------------------------------------

// A working credential is reported with what the model said, and the request
// carries the identity line and OAuth capability the subscription requires.
func TestProbeReportsAWorkingCredential(t *testing.T) {
	client, rt := upstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("request-id", "req_ok")
		_, _ = io.WriteString(w, `{"model":"claude-haiku-4-5","stop_reason":"end_turn",
			"content":[{"type":"text","text":" pong "},{"type":"tool_use","text":"ignored"}],
			"usage":{"input_tokens":12,"output_tokens":3}}`)
	})

	res := Probe(context.Background(), client, "tok", "")
	if !res.OK || res.Status != 200 || res.Reply != "pong" || res.Model != "claude-haiku-4-5" ||
		res.StopReason != "end_turn" || res.InputTokens != 12 || res.OutputTokens != 3 ||
		res.RequestID != "req_ok" || res.Error != "" {
		t.Errorf("probe result: %+v", res)
	}

	got := rt.last(t)
	if got.host != "api.anthropic.com" || got.path != "/v1/messages" || got.rawQuery != "beta=true" {
		t.Errorf("probe sent to %s%s?%s", got.host, got.path, got.rawQuery)
	}
	if got.header.Get("Authorization") != "Bearer tok" || got.header.Get("anthropic-beta") != oauthBeta ||
		got.header.Get("anthropic-version") != "2023-06-01" {
		t.Errorf("probe headers: %v", got.header)
	}
	var body struct {
		Model  string `json:"model"`
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
	}
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatal(err)
	}
	// An empty model falls back to the cheapest one; a probe must not spend
	// the operator's Opus allowance.
	if body.Model != "claude-haiku-4-5" {
		t.Errorf("default model = %q", body.Model)
	}
	// Inference on a subscription token is refused without the identity line.
	if len(body.System) != 1 || !strings.Contains(body.System[0].Text, "Claude Code") {
		t.Errorf("system = %+v", body.System)
	}
}

// The upstream's refusal is the only thing that tells an expired token from a
// plan restriction, so it is shown verbatim, bounded so a page of HTML does
// not swamp the screen.
func TestProbeSurfacesTheRefusalVerbatim(t *testing.T) {
	long := strings.Repeat("x", 3000)
	client, _ := upstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "  "+long+"  ")
	})
	res := Probe(context.Background(), client, "tok", "claude-opus-5")
	if res.OK || res.Status != http.StatusForbidden {
		t.Fatalf("probe: %+v", res)
	}
	if res.Error != long[:2000]+"…" {
		t.Errorf("error is %d bytes, want the first 2000 and an ellipsis", len(res.Error))
	}

	client, rt := upstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"token expired"}}`)
	})
	res = Probe(context.Background(), client, "tok", "claude-opus-5")
	if res.Error != `{"error":{"message":"token expired"}}` {
		t.Errorf("short refusal = %q", res.Error)
	}
	var body struct{ Model string }
	_ = json.Unmarshal(rt.last(t).body, &body)
	if body.Model != "claude-opus-5" {
		t.Errorf("named model replaced with %q", body.Model)
	}
}

// A 200 that is not a message, or no answer at all, is a failed probe — not a
// working credential with an empty reply.
func TestProbeFailures(t *testing.T) {
	client, _ := upstream(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "not json")
	})
	res := Probe(context.Background(), client, "tok", "")
	if res.OK || !strings.Contains(res.Error, "decode response") || res.Status != 200 {
		t.Errorf("undecodable: %+v", res)
	}

	dead := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("no route")
	})}
	res = Probe(context.Background(), dead, "tok", "")
	if res.OK || !strings.Contains(res.Error, "request failed") || res.Status != 0 {
		t.Errorf("unreachable: %+v", res)
	}

	broken := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(failingReader{})}, nil
	})}
	res = Probe(context.Background(), broken, "tok", "")
	if res.OK || !strings.Contains(res.Error, "read response") || res.Status != 200 {
		t.Errorf("truncated: %+v", res)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

// --- account usage ----------------------------------------------------------

// The usage endpoint is what /usage prints; the request must look like the
// client's and the answer must arrive with the figures the pool routes on.
func TestFetchUsage(t *testing.T) {
	client, rt := upstream(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"five_hour":{"utilization":12.5,"resets_at":"2026-10-01T14:00:00Z"},
			"seven_day":{"utilization":40},
			"limits":[{"kind":"weekly_scoped","percent":91,"scope":{"model":{"display_name":"Opus"}}}],
			"extra_usage":{"is_enabled":false}}`)
	})
	before := time.Now().UTC()
	u, err := FetchUsage(context.Background(), client, "tok")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	got := rt.last(t)
	if got.method != http.MethodGet || got.host != "api.anthropic.com" || got.path != usagePath {
		t.Errorf("sent %s %s%s", got.method, got.host, got.path)
	}
	if got.header.Get("Authorization") != "Bearer tok" || got.header.Get("anthropic-beta") != oauthBeta {
		t.Errorf("headers: %v", got.header)
	}
	if u.FiveHour == nil || u.FiveHour.Utilization != 12.5 || u.SevenDay == nil || len(u.Limits) != 1 ||
		u.ExtraUsage == nil {
		t.Fatalf("decoded: %+v", u)
	}
	if u.FetchedAt.Before(before) || u.FetchedAt.Location() != time.UTC {
		t.Errorf("FetchedAt = %s", u.FetchedAt)
	}
	// The scoped weekly limit binds before either headline window does.
	if got := u.Utilization(); got != 91 {
		t.Errorf("utilization = %v, want the scoped 91", got)
	}
}

// A refusal must be an error with the upstream's words, bounded, so the
// screen shows the previous figure rather than zeros.
func TestFetchUsageRefusals(t *testing.T) {
	long := strings.Repeat("y", 500)
	client, _ := upstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, long)
	})
	_, err := FetchUsage(context.Background(), client, "tok")
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.HasSuffix(err.Error(), long[:200]+"…") {
		t.Errorf("err = %v", err)
	}

	client, _ = upstream(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "{nope")
	})
	if _, err := FetchUsage(context.Background(), client, "tok"); err == nil || !strings.Contains(err.Error(), "parse usage") {
		t.Errorf("unparseable: %v", err)
	}

	dead := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("down")
	})}
	if _, err := FetchUsage(context.Background(), dead, "tok"); err == nil || !strings.Contains(err.Error(), "fetch usage") {
		t.Errorf("unreachable: %v", err)
	}

	broken := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(failingReader{})}, nil
	})}
	if _, err := FetchUsage(context.Background(), broken, "tok"); err == nil || !strings.Contains(err.Error(), "read usage") {
		t.Errorf("truncated: %v", err)
	}

	if got := trimForError([]byte("short")); got != "short" {
		t.Errorf("trimForError changed a short body: %q", got)
	}
}

// -1 is "nothing reported", which the pool must not read as an idle account.
func TestUtilizationAndLocked(t *testing.T) {
	if got := (AccountUsage{}).Utilization(); got != -1 {
		t.Errorf("empty utilization = %v, want -1", got)
	}
	u := AccountUsage{FiveHour: &UsageWindow{Utilization: 80}, SevenDay: &UsageWindow{Utilization: 30}}
	if got := u.Utilization(); got != 80 {
		t.Errorf("utilization = %v, want the worse window", got)
	}
	if u.Locked() {
		t.Error("an account with no locked reason reads as locked")
	}
	u.SevenDay.LockedReason = strp("")
	if u.Locked() {
		t.Error("an empty locked reason reads as locked")
	}
	u.SevenDay.LockedReason = strp("weekly_limit")
	if !u.Locked() {
		t.Error("a named locked reason is not reported")
	}
}

// The labels must match the client's own /usage display, so the operator
// sees the same words in both places.
func TestUsageLimitTitle(t *testing.T) {
	scoped := UsageLimit{Kind: "weekly_scoped"}
	scoped.Scope = &struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	}{Model: &struct {
		DisplayName string `json:"display_name"`
	}{DisplayName: "Fable"}}

	for _, c := range []struct {
		l    UsageLimit
		want string
	}{
		{UsageLimit{Kind: "session"}, "Current session"},
		{UsageLimit{Kind: "weekly_all"}, "Current week (all models)"},
		{scoped, "Current week (Fable)"},
		{UsageLimit{Kind: "weekly_scoped"}, "Current week (scoped)"},
		{UsageLimit{Kind: "something_new"}, "something_new"},
	} {
		if got := c.l.Title(); got != c.want {
			t.Errorf("%s: %q, want %q", c.l.Kind, got, c.want)
		}
	}
}

// --- wire -------------------------------------------------------------------

// The OAuth capability is merged into whatever the client sent: its own betas
// are its capabilities, and dropping the OAuth one fails the request with 401.
func TestAuthorizeMergesTheOAuthBeta(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", oauthBeta},
		{"prompt-caching-2024-07-31", oauthBeta + ",prompt-caching-2024-07-31"},
		{"x, " + oauthBeta, "x, " + oauthBeta},
	} {
		req, _ := http.NewRequest(http.MethodPost, "http://x/v1/messages", nil)
		if c.in != "" {
			req.Header.Set("anthropic-beta", c.in)
		}
		Provider{}.Authorize(req, "tok")
		if got := req.Header.Get("anthropic-beta"); got != c.want {
			t.Errorf("beta %q -> %q, want %q", c.in, got, c.want)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		if got := req.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("default version = %q", got)
		}
	}

	// A version the caller chose is its own and is not overwritten.
	req, _ := http.NewRequest(http.MethodPost, "http://x/v1/messages", nil)
	req.Header.Set("anthropic-version", "2099-01-01")
	Provider{}.Authorize(req, "tok")
	if got := req.Header.Get("anthropic-version"); got != "2099-01-01" {
		t.Errorf("caller's version replaced with %q", got)
	}
	if (Provider{}).BaseURL() != "https://api.anthropic.com" {
		t.Error("base URL moved")
	}
}

// The meter is chosen by the shape of the answer, not by what was asked: a
// streaming request that fails early answers in plain JSON.
func TestMeterFollowsTheAnswersShape(t *testing.T) {
	var usage provider.Usage
	var streamErr string
	var first time.Time
	into := provider.Reading{Usage: &usage, StreamError: &streamErr, FirstContent: &first}

	m := Provider{}.Meter("text/event-stream; charset=utf-8", into)
	m.Feed([]byte("event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":7}}}\n\n" +
		"event: content_block_delta\ndata: {}\n\n"))
	m.Done()
	if usage.InputTokens != 7 {
		t.Errorf("stream meter read input = %d", usage.InputTokens)
	}
	// The time to first token is recorded on the first content event, not on
	// the quiet opening.
	if first.IsZero() {
		t.Error("first content time not recorded")
	}

	usage = provider.Usage{}
	m = Provider{}.Meter("application/json", into)
	m.Feed([]byte(`{"usage":{"input_tokens":3,"output_tokens":4}}`))
	m.Done()
	if usage.InputTokens != 3 || usage.OutputTokens != 4 {
		t.Errorf("json meter: %+v", usage)
	}
}

// The stall detector holds a stream until it carries content; mislabelling
// an opening event as content would release a dead stream, and mislabelling
// content as quiet would abandon a live one.
func TestEventKinds(t *testing.T) {
	for name, want := range map[string]provider.EventKind{
		"message_start":       provider.EventQuiet,
		"content_block_start": provider.EventQuiet,
		"ping":                provider.EventQuiet,
		"error":               provider.EventError,
		"content_block_stop":  provider.EventSettling,
		"message_delta":       provider.EventSettling,
		"message_stop":        provider.EventSettling,
		"content_block_delta": provider.EventContent,
		"something_new":       provider.EventContent,
	} {
		if got := (Provider{}).Event([]byte(name)); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
}

// --- error codes ------------------------------------------------------------

// One word per outcome, shared by the request log, its menu and its filter.
// The content check must win over the invalid_request_error it is dressed
// as, because the specific answer is the one an operator can act on.
func TestErrorCode(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   string
	}{
		{200, "", ""},
		{400, `{"type":"error","error":{"type":"invalid_request_error","message":"Third-party apps now draw from your extra usage, not your plan limits."}}`, "content_check"},
		{429, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, "rate_limit_error"},
		{529, `{"type" : "error", "error": {"type": "overloaded_error"}}`, "overloaded_error"},
		{0, "dial tcp: connection refused", "no_answer"},
		{502, "<html>bad gateway</html>", "other"},
		{500, `{"type":"error"}`, "other"},
	} {
		if got := ErrorCode(c.status, c.body); got != c.want {
			t.Errorf("ErrorCode(%d, %.40q) = %q, want %q", c.status, c.body, got, c.want)
		}
	}
}
