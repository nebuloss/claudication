package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"claudication/internal/store"
)

// upstreamModels is the model list the fake upstream answers for every
// account: one dated id, to prove a switch covers its alias.
const upstreamModels = `{"data":[
	{"type":"model","id":"claude-opus-5-5","display_name":"Claude Opus 5.5","max_input_tokens":1000000},
	{"type":"model","id":"claude-opus-5","display_name":"Claude Opus 5"},
	{"type":"model","id":"claude-haiku-4-5-20251001","display_name":"Claude Haiku 4.5"}],
	"has_more":false,"first_id":"claude-opus-5-5","last_id":"claude-haiku-4-5-20251001"}`

// Switching a model off for an account takes it out of that account's
// rotation at once: a request for it goes to an account that has it on, is
// refused as a model this gateway does not serve when none has, and drops out
// of the list clients build their menus from.
func TestModelSwitchesOverTheAPI(t *testing.T) {
	srv, st, _ := newTestServer(t)

	var (
		mu      sync.Mutex
		bearers []string
	)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		bearers = append(bearers, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m",
			"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",
			"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer up.Close()
	srv.relay.BaseURL = up.URL
	// The model list goes through the gateway's own client.
	srv.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(rec, upstreamModels)
		} else {
			rec.WriteHeader(http.StatusNotFound)
		}
		return rec.Result(), nil
	})

	first := addAccount(t, srv, st, "first@example.com", "first-access", "r1", time.Now().Add(time.Hour))
	second := addAccount(t, srv, st, "second@example.com", "second-access", "r2", time.Now().Add(time.Hour))
	_, key, err := st.CreateKey(context.Background(), "k", store.KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	base, c := adminAPI(t, srv)

	set := func(account, model string, on bool) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"model": model, "enabled": on})
		if status, body := call(t, base, http.MethodPost, "/admin/accounts/"+account+"/models", string(b), c); status != http.StatusOK {
			t.Fatalf("switch %s on %s: %d %s", model, account, status, body)
		}
	}
	ask := func(model string) (int, []byte, string) {
		t.Helper()
		mu.Lock()
		before := len(bearers)
		mu.Unlock()
		resp := post(t, base+"/v1/messages", key,
			`{"model":"`+model+`","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		mu.Lock()
		defer mu.Unlock()
		served := ""
		if len(bearers) > before {
			served = bearers[len(bearers)-1]
		}
		return resp.StatusCode, body, served
	}
	listed := func() []string {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
		req.Header.Set("X-Api-Key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var l struct {
			Data    []struct{ ID string } `json:"data"`
			HasMore *bool                 `json:"has_more"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&l); err != nil {
			t.Fatal(err)
		}
		if l.HasMore == nil {
			t.Error("filtering dropped the list's other fields")
		}
		ids := []string{}
		for _, m := range l.Data {
			ids = append(ids, m.ID)
		}
		return ids
	}

	// Off for the first account: served by the second.
	set(first.ID, "claude-opus-5", false)
	if status, body, served := ask("claude-opus-5"); status != http.StatusOK || served != "second-access" {
		t.Errorf("opus-5, off on the first: %d %s, served by %q", status, body, served)
	}
	if _, _, served := ask("claude-opus-5-5"); served != "first-access" {
		t.Errorf("another model left the first account: served by %q", served)
	}

	// The account's own list says which are on, and the accounts screen says
	// which are off.
	status, body := call(t, base, http.MethodGet, "/admin/accounts/"+first.ID+"/models", "", c)
	if status != http.StatusOK {
		t.Fatalf("account models: %d %s", status, body)
	}
	got := map[string]bool{}
	for _, m := range decode[struct {
		Models []struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
			Listed  bool   `json:"listed"`
		} `json:"models"`
	}](t, body).Models {
		got[m.ID] = m.Enabled
		if !m.Listed {
			t.Errorf("%s reported unlisted", m.ID)
		}
	}
	if got["claude-opus-5"] || !got["claude-opus-5-5"] || !got["claude-haiku-4-5-20251001"] {
		t.Errorf("first account's models = %v", got)
	}
	_, body = call(t, base, http.MethodGet, "/admin/accounts", "", c)
	for _, a := range decode[accountsList](t, body).Accounts {
		if want := a.ID == first.ID; slices.Contains(a.ModelsOff, "claude-opus-5") != want {
			t.Errorf("%s models_off = %v", a.Email, a.ModelsOff)
		}
	}

	// Off everywhere, switched by its dated id and asked for by its alias:
	// refused in Anthropic's own shape, and gone from the list.
	set(first.ID, "claude-haiku-4-5-20251001", false)
	set(second.ID, "claude-haiku-4-5-20251001", false)
	status, body, served := ask("claude-haiku-4-5")
	if status != http.StatusNotFound || errType(t, body) != "not_found_error" || served != "" ||
		!strings.Contains(string(body), "turned off") {
		t.Errorf("haiku off everywhere: %d %s (served by %q)", status, body, served)
	}
	if ids := listed(); !slices.Equal(ids, []string{"claude-opus-5-5", "claude-opus-5"}) {
		t.Errorf("/v1/models = %v; want haiku gone, opus-5 kept for the second account", ids)
	}

	// On again, by its alias, on one account: back in service and in the list.
	set(second.ID, "claude-haiku-4-5", true)
	if status, _, served := ask("claude-haiku-4-5"); status != http.StatusOK || served != "second-access" {
		t.Errorf("haiku back on the second: %d, served by %q", status, served)
	}
	if ids := listed(); len(ids) != 3 {
		t.Errorf("/v1/models = %v, want all three", ids)
	}

	// The switch endpoint refuses what it cannot act on.
	for path, b := range map[string]string{
		"/admin/accounts/" + first.ID + "/models": `{"model":"m"}`,
		"/admin/accounts/nope/models":             `{"model":"m","enabled":false}`,
	} {
		if status, _ := call(t, base, http.MethodPost, path, b, c); status == http.StatusOK {
			t.Errorf("POST %s %s was accepted", path, b)
		}
	}
	if status, _ := call(t, base, http.MethodGet, "/admin/accounts/nope/models", "", c); status != http.StatusNotFound {
		t.Errorf("models of an unknown account: %d", status)
	}
}

// Accounts on different plans list different models. The gateway serves the
// union, each account's list less what is off on that account, so a model only
// the second account's plan has is still offered — and goes once the only
// account that has it switches it off.
func TestModelListIsTheUnionOfAccounts(t *testing.T) {
	srv, st, _ := newTestServer(t)
	srv.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		switch strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") {
		case "pro-access":
			_, _ = io.WriteString(rec, `{"data":[{"id":"claude-sonnet-5"},{"id":"claude-haiku-4-5-20251001"}],"has_more":false}`)
		case "max-access":
			_, _ = io.WriteString(rec, `{"data":[{"id":"claude-opus-5-5"},{"id":"claude-sonnet-5"}],"has_more":false}`)
		default: // a dead account: skipped, not fatal
			rec.WriteHeader(http.StatusUnauthorized)
		}
		return rec.Result(), nil
	})
	addAccount(t, srv, st, "pro@example.com", "pro-access", "r1", time.Now().Add(time.Hour))
	maxAcct := addAccount(t, srv, st, "max@example.com", "max-access", "r2", time.Now().Add(time.Hour))
	addAccount(t, srv, st, "dead@example.com", "dead-access", "r3", time.Now().Add(time.Hour))

	ids := func() []string {
		t.Helper()
		body, err := srv.gateway.FetchModels(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var l struct {
			Data    []struct{ ID string } `json:"data"`
			HasMore bool                  `json:"has_more"`
			FirstID string                `json:"first_id"`
		}
		if err := json.Unmarshal(body, &l); err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, m := range l.Data {
			out = append(out, m.ID)
		}
		if l.HasMore || (len(out) > 0 && l.FirstID != out[0]) {
			t.Errorf("paging fields do not describe the merged list: %+v", l)
		}
		return out
	}

	want := []string{"claude-sonnet-5", "claude-haiku-4-5-20251001", "claude-opus-5-5"}
	if got := ids(); !slices.Equal(got, want) {
		t.Errorf("models = %v, want %v: the first account's, then what the second adds", got, want)
	}
	if err := st.SetAccountModel(context.Background(), maxAcct.ID, "claude-opus-5-5", false); err != nil {
		t.Fatal(err)
	}
	if got := ids(); slices.Contains(got, "claude-opus-5-5") {
		t.Errorf("models = %v: opus-5-5 is off on the only account that has it", got)
	}
}
