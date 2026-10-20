package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"claudication/internal/httpapi/admin"
	"claudication/internal/httpapi/gateway"
	"claudication/internal/store"
)

// The config screen answers "why is it set to that", so it has to report the
// runtime switches beside the file settings.
func TestConfigReportsSettingsAndSwitches(t *testing.T) {
	srv, _, _ := newTestServer(t)
	base, c := adminAPI(t, srv)

	status, body := call(t, base, http.MethodGet, "/admin/config", "", c)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	got := decode[map[string]any](t, body)
	for _, k := range []string{"path", "settings", "gateway_enabled"} {
		if _, ok := got[k]; !ok {
			t.Errorf("config is missing %q", k)
		}
	}
	if got["gateway_enabled"] != true {
		t.Errorf("a fresh gateway has its switch the wrong way: %v", got)
	}
	// Image fitting is each client's now (TestImageFittingIsPerClient).
	if _, ok := got["fit_images"]; ok {
		t.Errorf("config still reports a gateway-wide image switch: %v", got)
	}
}

// The setup screen lists models through the session, not a key; an upstream
// it cannot reach is a 502, never an empty list that reads as "no models".
func TestAdminModels(t *testing.T) {
	srv, st, _ := newTestServer(t)
	fake := &fakeAnthropic{}
	fake.install(srv)
	if err := seedAccount(t, st, srv); err != nil {
		t.Fatal(err)
	}
	base, c := adminAPI(t, srv)

	status, body := call(t, base, http.MethodGet, "/admin/models", "", c)
	if status != http.StatusOK || !strings.Contains(string(body), "claude-opus-5") {
		t.Fatalf("models: %d %s", status, body)
	}
	fake.set(func(f *fakeAnthropic) { f.modelsFail = true })
	if status, body := call(t, base, http.MethodGet, "/admin/models", "", c); status != http.StatusBadGateway {
		t.Errorf("failing upstream: %d %s", status, body)
	}
}

// The gateway switched off over the API is off for the next request on every
// client-facing route, each refusing in its own dialect's envelope; the admin
// API goes on answering, and the switch is stored, so it survives a restart.
func TestGatewaySwitch(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, c := adminAPI(t, srv)

	status, body := call(t, base, http.MethodPost, "/admin/gateway", `{"enabled":false}`, c)
	if status != http.StatusOK || decode[map[string]any](t, body)["gateway_enabled"] != false {
		t.Fatalf("status = %d: %s", status, body)
	}
	if v, ok, _ := st.Setting(context.Background(), gateway.Setting); !ok || v != "false" {
		t.Errorf("stored = %q, %v", v, ok)
	}
	for _, rt := range []struct{ method, path, envelope string }{
		{http.MethodGet, "/v1/models", `"type":"error"`},
		{http.MethodPost, "/v1/messages", `"type":"error"`},
		{http.MethodPost, "/v1/messages/count_tokens", `"type":"error"`},
		{http.MethodPost, "/v1/responses", `"code":`},
	} {
		status, body := call(t, base, rt.method, rt.path, `{}`, nil)
		if status != http.StatusNotFound || !strings.Contains(string(body), "switched off") ||
			!strings.Contains(string(body), rt.envelope) {
			t.Errorf("%s %s with the gateway off: %d %s", rt.method, rt.path, status, body)
		}
	}
	if status, _ := call(t, base, http.MethodGet, "/admin/config", "", c); status != http.StatusOK {
		t.Errorf("the admin API stopped with the gateway: %d", status)
	}
	// Up and switched off: healthy, and saying it does not accept requests,
	// which the welcome page shows and a monitor can tell from being down.
	health := func() map[string]any {
		status, body := call(t, base, http.MethodGet, "/health", "", nil)
		if status != http.StatusOK {
			t.Fatalf("health: %d %s", status, body)
		}
		return decode[map[string]any](t, body)
	}
	if h := health(); h["status"] != "ok" || h["accepting"] != false {
		t.Errorf("health with the gateway off = %v", h)
	}
	if status, body := call(t, base, http.MethodPost, "/admin/gateway", `{"enabled":true}`, c); status != http.StatusOK {
		t.Fatalf("on again: %d %s", status, body)
	}
	if h := health(); h["accepting"] != true {
		t.Errorf("health with the gateway on = %v", h)
	}
	if status, _ := call(t, base, http.MethodPost, "/v1/messages", `{}`, nil); status == http.StatusNotFound {
		t.Error("the gateway switched back on still refuses")
	}
	for _, b := range []string{`{}`, `nope`} {
		if status, _ := call(t, base, http.MethodPost, "/admin/gateway", b, c); status != http.StatusBadRequest {
			t.Errorf("%s: %d", b, status)
		}
	}
}

// The gateway switch refuses a body that does not say which way to flip it,
// and a manual chat title is trimmed and must not be empty. The gateway-wide
// image switch is gone: fitting is each client's (TestImageFittingIsPerClient).
func TestRuntimeSwitches(t *testing.T) {
	srv, _, _ := newTestServer(t)
	base, c := adminAPI(t, srv)

	status, body := call(t, base, http.MethodPut, "/admin/chats/c1/title", `{"title":"  ZeeOS installer "}`, c)
	if got := decode[map[string]any](t, body); status != http.StatusOK || got["title"] != "ZeeOS installer" {
		t.Errorf("manual title: %d %s", status, body)
	}
	status, body = call(t, base, http.MethodPut, "/admin/chats/c1/title", `{"title":"  "}`, c)
	if status != http.StatusBadRequest {
		t.Errorf("empty manual title: %d %s", status, body)
	}

	for _, bad := range []string{`{}`, `{"enabled":`, `{"enabled":"yes"}`} {
		if status, _ := call(t, base, http.MethodPost, "/admin/gateway", bad, c); status != http.StatusBadRequest {
			t.Errorf("/admin/gateway %s: %d, want 400", bad, status)
		}
	}
	if status, _ := call(t, base, http.MethodPost, "/admin/fit-images", `{"enabled":true}`, c); status == http.StatusOK {
		t.Error("the gateway-wide image switch still answers")
	}
}

// Editing a key over the API changes its name and limits and takes effect on
// the next request; the secret does not change, so nothing holding the key has
// to be told.
func TestUpdateKeyOverTheAPI(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, c := adminAPI(t, srv)
	ctx := context.Background()
	key, plaintext, err := st.CreateKey(ctx, "before", store.KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}

	status, body := call(t, base, http.MethodPatch, "/admin/keys/"+key.ID,
		`{"name":"  after  ","rpm_limit":7,"rate_period_s":3600,"token_budget":500}`, c)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	got := decode[struct {
		Key admin.KeyJSON `json:"key"`
	}](t, body).Key
	if got.Name != "after" || got.RPMLimit != 7 || got.RatePeriodS != 3600 || got.TokenBudget != 500 {
		t.Errorf("answer = %+v", got)
	}
	again, err := st.Authenticate(ctx, plaintext)
	if err != nil {
		t.Fatalf("the secret stopped working: %v", err)
	}
	if again.Name != "after" || again.RPMLimit != 7 || again.Period() != time.Hour || again.TokenBudget != 500 {
		t.Errorf("stored = %+v", again)
	}

	for _, bad := range []string{
		`{"name":""}`,
		`{"name":"x","rpm_limit":-1}`,
		`{"name":"x","token_budget":-1}`,
		`{"name":"x","rate_period_s":9999999}`,
		`{`,
	} {
		if status, _ := call(t, base, http.MethodPatch, "/admin/keys/"+key.ID, bad, c); status != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, status)
		}
	}
	if status, _ := call(t, base, http.MethodPatch, "/admin/keys/nope", `{"name":"x"}`, c); status != http.StatusNotFound {
		t.Errorf("unknown key: %d", status)
	}
}

// The curl-friendly sign-in path explains itself when called without a link,
// and a flood of guesses at links is throttled before the store is asked.
func TestTokenSessionEndpoint(t *testing.T) {
	srv, st, _ := newTestServer(t)
	srv.cfg.Limits.AnonPerMinute = 2
	ts := adminOnly(t, srv)

	if status, body := call(t, ts, http.MethodGet, "/admin/session", "", nil); status != http.StatusBadRequest ||
		errType(t, body) != "invalid_request" {
		t.Errorf("no token: %d %s", status, body)
	}

	token := mintLink(t, st, store.LoginLinkTTL)
	resp, err := noRedirectClient().Get(ts + "/admin/session?token=" + token)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("spend: %d → %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if c := sessionCookieOf(t, resp); !c.HttpOnly {
		t.Error("the session cookie is readable from JavaScript")
	}

	limited := false
	for i := 0; i < 5; i++ {
		status, body := call(t, ts, http.MethodGet, "/admin/session?token=guess", "", nil)
		if status == http.StatusTooManyRequests {
			limited = errType(t, body) == "rate_limit"
			break
		}
	}
	if !limited {
		t.Error("guessing sign-in links was never throttled")
	}
}
