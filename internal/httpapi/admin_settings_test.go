package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"claudication/internal/config"
	"claudication/internal/service/titles"
	"claudication/internal/store"
)

// The config screen answers "why is it set to that", so it has to report the
// runtime switches beside the file settings, and the docs link it renders has
// to point where the page actually is.
func TestConfigReportsSettingsAndSwitches(t *testing.T) {
	srv, _, _ := newTestServer(t)
	srv.cfg.PublicURL = "https://gw.example"
	base, c := adminAPI(t, srv)

	status, body := call(t, base, http.MethodGet, "/admin/config", "", c)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	got := decode[map[string]any](t, body)
	for _, k := range []string{"path", "settings", "surfaces", "chat_titles", "chat_titles_capture", "fit_images", "docs_enabled"} {
		if _, ok := got[k]; !ok {
			t.Errorf("config is missing %q", k)
		}
	}
	if got["docs_url"] != "https://gw.example" {
		t.Errorf("docs_url = %v, want the relay's public address when docs share it", got["docs_url"])
	}
	if got["fit_images"] != false || got["docs_enabled"] != false || got["chat_titles"] != false {
		t.Errorf("a fresh gateway has a switch on: %v", got)
	}
}

// Where the docs page has its own listener, the link is that listener's
// public address, not the relay's.
func TestDocsURLFollowsTheDocsListener(t *testing.T) {
	cfg := config.Defaults()
	cfg.PublicURL = "https://relay.example"
	if got := docsURL(cfg); got != "https://relay.example" {
		t.Errorf("shared: %q", got)
	}
	cfg.DocsListen = "127.0.0.1:0"
	cfg.DocsURL = "https://docs.example"
	if got := docsURL(cfg); got != "https://docs.example" {
		t.Errorf("own listener: %q", got)
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

// A surface switched off over the API is off for the next request and stays
// off across a restart; the answer is the whole state, so the UI redraws from
// what the server holds rather than from what it asked for.
func TestSetSurfaceOverTheAPI(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, c := adminAPI(t, srv)

	status, body := call(t, base, http.MethodPost, "/admin/surfaces/openai", `{"enabled":false}`, c)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	got := decode[struct {
		Surfaces []surfaceState `json:"surfaces"`
	}](t, body)
	seen := false
	for _, s := range got.Surfaces {
		if s.ID == "openai" {
			seen = true
			if s.Enabled {
				t.Error("openai still enabled in the answer")
			}
		}
	}
	if !seen {
		t.Errorf("surfaces = %+v", got.Surfaces)
	}
	if v, ok, _ := st.Setting(context.Background(), surfaceKey("openai")); !ok || v != "false" {
		t.Errorf("stored = %q, %v", v, ok)
	}
	// And the relay honours it at once.
	if status, _ := call(t, base, http.MethodPost, "/v1/responses", `{}`, nil); status != http.StatusNotFound {
		t.Errorf("a switched-off surface answered %d", status)
	}

	for _, tc := range []struct{ path, body string }{
		{"/admin/surfaces/openai", `{}`},
		{"/admin/surfaces/openai", `nope`},
	} {
		if status, _ := call(t, base, http.MethodPost, tc.path, tc.body, c); status != http.StatusBadRequest {
			t.Errorf("%s %s: %d", tc.path, tc.body, status)
		}
	}
	if status, _ := call(t, base, http.MethodPost, "/admin/surfaces/grpc", `{"enabled":true}`, c); status != http.StatusNotFound {
		t.Errorf("unknown surface: %d", status)
	}
}

// Each of these switches decides what the gateway may do on its own — spend
// the subscription naming chats, re-encode a caller's image, publish a page —
// so each must persist, report its new state, and refuse a body that does not
// say which way to flip it.
func TestRuntimeSwitches(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, c := adminAPI(t, srv)
	ctx := context.Background()

	stored := func(key string) string {
		v, _, err := st.Setting(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	status, body := call(t, base, http.MethodPost, "/admin/fit-images", `{"enabled":true}`, c)
	if status != http.StatusOK || decode[map[string]bool](t, body)["fit_images"] != true {
		t.Errorf("fit-images: %d %s", status, body)
	}
	if !srv.images.On() || stored(imageFitSetting) != "true" {
		t.Error("fit-images did not take effect or did not persist")
	}

	status, body = call(t, base, http.MethodPost, "/admin/docs", `{"enabled":true}`, c)
	if status != http.StatusOK || decode[map[string]bool](t, body)["docs_enabled"] != true {
		t.Errorf("docs: %d %s", status, body)
	}
	if stored(docsSetting) != "true" {
		t.Error("docs switch did not persist")
	}

	// Capture alone, then generation alone: either may be sent without the
	// other, and sending one leaves the other where it was.
	status, body = call(t, base, http.MethodPost, "/admin/chat-titles", `{"capture":true}`, c)
	if status != http.StatusOK {
		t.Fatalf("chat-titles: %d %s", status, body)
	}
	if got := decode[map[string]bool](t, body); !got["chat_titles_capture"] || got["chat_titles"] {
		t.Errorf("after capture: %v", got)
	}
	status, body = call(t, base, http.MethodPost, "/admin/chat-titles", `{"enabled":true}`, c)
	if got := decode[map[string]bool](t, body); status != http.StatusOK || !got["chat_titles_capture"] || !got["chat_titles"] {
		t.Errorf("after enable: %d %v", status, got)
	}
	if stored(titles.SettingGenerate) != "true" || stored(titles.SettingCapture) != "true" {
		t.Error("chat-title switches did not persist")
	}

	for _, path := range []string{"/admin/fit-images", "/admin/docs", "/admin/chat-titles"} {
		for _, bad := range []string{`{}`, `{"enabled":`, `{"enabled":"yes"}`} {
			if status, _ := call(t, base, http.MethodPost, path, bad, c); status != http.StatusBadRequest {
				t.Errorf("%s %s: %d, want 400", path, bad, status)
			}
		}
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
		Key keyJSON `json:"key"`
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
