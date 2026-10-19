package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"claudication/internal/store"
)

// The model roles, end to end: automatic follows the newest of the family, a
// gateway binding moves every key, a key's binding moves only that key, the
// client is answered in the role's name, a binding that cannot serve is
// refused before anything is sent, and the model list a key is offered leads
// with the roles as they serve for it.
func TestModelRolesOverTheAPI(t *testing.T) {
	srv, st, _ := newTestServer(t)

	var (
		mu     sync.Mutex
		models []string
	)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		models = append(models, req.Model)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"`+req.Model+`",
			"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",
			"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer up.Close()
	srv.relay.BaseURL = up.URL
	srv.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(rec, upstreamModels)
		} else {
			rec.WriteHeader(http.StatusNotFound)
		}
		return rec.Result(), nil
	})

	acct := addAccount(t, srv, st, "first@example.com", "first-access", "r1", time.Now().Add(time.Hour))
	laptop, laptopKey, err := st.CreateKey(context.Background(), "laptop", store.KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	_, otherKey, err := st.CreateKey(context.Background(), "other", store.KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	base, c := adminAPI(t, srv)

	bind := func(keyID, role, model string) {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"key_id": keyID, "role": role, "model": model})
		if status, body := call(t, base, http.MethodPut, "/admin/model-roles", string(b), c); status != http.StatusNoContent {
			t.Fatalf("bind %s %s → %s: %d %s", keyID, role, model, status, body)
		}
	}
	ask := func(key, model string) (int, string, string) {
		t.Helper()
		resp := post(t, base+"/v1/messages", key,
			`{"model":"`+model+`","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)
		defer resp.Body.Close()
		var answer struct {
			Model string `json:"model"`
		}
		body, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(body, &answer)
		mu.Lock()
		defer mu.Unlock()
		sent := ""
		if len(models) > 0 {
			sent = models[len(models)-1]
		}
		return resp.StatusCode, sent, answer.Model
	}

	// Automatic: the newest Opus and the newest Haiku the accounts list.
	if status, sent, answered := ask(laptopKey, "claude-large"); status != http.StatusOK ||
		sent != "claude-opus-5-5" || answered != "claude-large" {
		t.Errorf("large, automatic: %d, sent %q, answered %q", status, sent, answered)
	}
	if _, sent, _ := ask(laptopKey, "claude-small"); sent != "claude-haiku-4-5-20251001" {
		t.Errorf("small, automatic: sent %q", sent)
	}

	// The gateway's binding moves every key; a key's moves only that key.
	bind("", "claude-large", "claude-opus-5")
	bind(laptop.ID, "claude-large", "claude-haiku-4-5")
	if _, sent, _ := ask(otherKey, "claude-large"); sent != "claude-opus-5" {
		t.Errorf("another key after the gateway's binding: sent %q", sent)
	}
	if _, sent, answered := ask(laptopKey, "claude-large"); sent != "claude-haiku-4-5" || answered != "claude-large" {
		t.Errorf("the key's own binding: sent %q, answered %q", sent, answered)
	}
	bind(laptop.ID, "claude-large", "")
	if _, sent, _ := ask(laptopKey, "claude-large"); sent != "claude-opus-5" {
		t.Errorf("key back on the gateway's: sent %q", sent)
	}

	// Bound to a model no account has on: refused, nothing sent, saying which.
	b, _ := json.Marshal(map[string]any{"model": "claude-opus-5", "enabled": false})
	if status, body := call(t, base, http.MethodPost, "/admin/accounts/"+acct.ID+"/models", string(b), c); status != http.StatusOK {
		t.Fatalf("switch off: %d %s", status, body)
	}
	before := len(models)
	resp := post(t, base+"/v1/messages", otherKey,
		`{"model":"claude-large","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "claude-opus-5") || len(models) != before {
		t.Errorf("binding that cannot serve: %d %s, upstream asked %d times", resp.StatusCode, body, len(models)-before)
	}

	// The log records both names.
	rows, _, err := st.RecentUsage(context.Background(), 50, store.UsageCursor{}, store.RequestFilter{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range rows {
		if e.VirtualModel == "claude-large" && e.Model == "claude-opus-5-5" {
			found = true
		}
	}
	if !found {
		t.Error("no request recorded as claude-large served by claude-opus-5-5")
	}

	// What a key is offered: the roles first, as they serve for it.
	bind("", "claude-large", "")
	req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
	req.Header.Set("X-Api-Key", laptopKey)
	lresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var l struct {
		Data []struct {
			ID      string `json:"id"`
			Serving string `json:"serving"`
			Role    string `json:"role"`
			Max     int    `json:"max_input_tokens"`
		} `json:"data"`
	}
	_ = json.NewDecoder(lresp.Body).Decode(&l)
	lresp.Body.Close()
	if len(l.Data) < 2 || l.Data[0].ID != "claude-large" || l.Data[0].Serving != "claude-opus-5-5" ||
		l.Data[0].Role != "large" || l.Data[0].Max != 1000000 || l.Data[1].ID != "claude-small" {
		t.Errorf("model list head: %+v", l.Data[:min(2, len(l.Data))])
	}

	// The admin view says what each role serves, for the gateway and a key.
	status, body := call(t, base, http.MethodGet, "/admin/model-roles?key="+laptop.ID, "", c)
	if status != http.StatusOK || !strings.Contains(string(body), `"serving":"claude-opus-5-5"`) {
		t.Errorf("roles for a key: %d %s", status, body)
	}

	for _, bad := range []string{
		`{"role":"Bad Name","model":"claude-opus-5"}`,
		`{"role":"claude-large","model":"claude-small"}`,
		`{"key_id":"` + laptop.ID + `","role":"never-made","model":"claude-opus-5"}`,
	} {
		if status, _ := call(t, base, http.MethodPut, "/admin/model-roles", bad, c); status != http.StatusBadRequest {
			t.Errorf("PUT %s: %d", bad, status)
		}
	}

	// The families are the accounts' own: claude-opus and claude-haiku exist
	// because models of them are listed, and nothing else does. The roles,
	// GPT ones included, are there whatever is listed.
	_, body = call(t, base, http.MethodGet, "/admin/model-roles", "", c)
	var listed struct {
		Roles []struct {
			Role    string `json:"role"`
			Builtin bool   `json:"builtin"`
			Serving string `json:"serving"`
		} `json:"roles"`
	}
	_ = json.Unmarshal(body, &listed)
	names := []string{}
	for _, r := range listed.Roles {
		names = append(names, r.Role)
	}
	if strings.Join(names, ",") != "claude-large,claude-small,gpt-large,gpt-small,claude-opus,claude-haiku" {
		t.Errorf("aliases = %v", names)
	}

	// A custom alias: made, served, shown to a key's clients, deleted.
	bind("", "team-fast", "claude-haiku-4-5")
	if _, sent, answered := ask(otherKey, "team-fast"); sent != "claude-haiku-4-5" || answered != "team-fast" {
		t.Errorf("custom alias: sent %q, answered %q", sent, answered)
	}
	bind(laptop.ID, "team-fast", "claude-opus-5-5")
	if _, sent, _ := ask(laptopKey, "team-fast"); sent != "claude-opus-5-5" {
		t.Errorf("custom alias, key's own binding: sent %q", sent)
	}
	if status, body := call(t, base, http.MethodDelete, "/admin/model-roles?name=claude-opus", "", c); status != http.StatusBadRequest {
		t.Errorf("deleting a built-in: %d %s", status, body)
	}
	if status, body := call(t, base, http.MethodDelete, "/admin/model-roles?name=team-fast", "", c); status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", status, body)
	}
	before = len(models)
	if _, _, _ = ask(otherKey, "team-fast"); len(models) != before+1 || models[len(models)-1] != "team-fast" {
		t.Errorf("a deleted alias is still mapped: sent %q", models[len(models)-1])
	}
}

// An alias bound per client, end to end: a request whose User-Agent is
// Codex's gets Codex's binding while Claude Code keeps the gateway's, a key's
// own binding still outranks it, /v1/models and a key's configs follow the
// client, the admin view lists it, and a binding that cannot serve says it
// was the client's.
func TestClientAliasesOverTheAPI(t *testing.T) {
	srv, st, _ := newTestServer(t)

	var (
		mu     sync.Mutex
		models []string
	)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		models = append(models, req.Model)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"`+req.Model+`",
			"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",
			"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer up.Close()
	srv.relay.BaseURL = up.URL
	srv.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(rec, upstreamModels)
		} else {
			rec.WriteHeader(http.StatusNotFound)
		}
		return rec.Result(), nil
	})

	acct := addAccount(t, srv, st, "first@example.com", "first-access", "r1", time.Now().Add(time.Hour))
	laptop, laptopKey, err := st.CreateKey(context.Background(), "laptop", store.KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	other, otherKey, err := st.CreateKey(context.Background(), "other", store.KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	base, c := adminAPI(t, srv)

	const (
		codexUA  = "codex_cli_rs/0.54.0"
		claudeUA = "claude-cli/2.1.274 (external, sdk-cli)"
	)
	put := func(v map[string]string) (int, []byte) {
		t.Helper()
		b, _ := json.Marshal(v)
		return call(t, base, http.MethodPut, "/admin/model-roles", string(b), c)
	}
	bind := func(v map[string]string) {
		t.Helper()
		if status, body := put(v); status != http.StatusNoContent {
			t.Fatalf("bind %v: %d %s", v, status, body)
		}
	}
	sentCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(models)
	}
	ask := func(key, ua string) (int, string, string) {
		t.Helper()
		resp := postHeaders(t, base+"/v1/messages", key,
			`{"model":"claude-large","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`,
			map[string]string{"User-Agent": ua})
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		mu.Lock()
		defer mu.Unlock()
		sent := ""
		if len(models) > 0 {
			sent = models[len(models)-1]
		}
		return resp.StatusCode, sent, string(body)
	}

	bind(map[string]string{"role": "claude-large", "model": "claude-opus-5"})
	bind(map[string]string{"client": "Codex", "role": "claude-large", "model": "claude-haiku-4-5"})

	if status, sent, body := ask(otherKey, codexUA); status != http.StatusOK || sent != "claude-haiku-4-5" ||
		!strings.Contains(body, `"model":"claude-large"`) {
		t.Errorf("Codex: %d, sent %q, answered %s", status, sent, body)
	}
	if _, sent, _ := ask(otherKey, claudeUA); sent != "claude-opus-5" {
		t.Errorf("Claude Code beside Codex's binding: sent %q", sent)
	}
	bind(map[string]string{"key_id": laptop.ID, "role": "claude-large", "model": "claude-opus-5-5"})
	if _, sent, _ := ask(laptopKey, codexUA); sent != "claude-opus-5-5" {
		t.Errorf("a key's binding under Codex: sent %q, want the key's", sent)
	}

	// /v1/models as Codex sees it, and a key's configs written for Codex.
	head := func(body []byte) (id, serving string) {
		var l struct {
			Data []struct {
				ID      string `json:"id"`
				Serving string `json:"serving"`
			} `json:"data"`
		}
		_ = json.Unmarshal(body, &l)
		if len(l.Data) == 0 {
			return "", ""
		}
		return l.Data[0].ID, l.Data[0].Serving
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
	req.Header.Set("X-Api-Key", otherKey)
	req.Header.Set("User-Agent", codexUA)
	lresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	listed, _ := io.ReadAll(lresp.Body)
	lresp.Body.Close()
	if id, serving := head(listed); id != "claude-large" || serving != "claude-haiku-4-5" {
		t.Errorf("/v1/models for Codex leads with %q serving %q", id, serving)
	}
	_, offered := call(t, base, http.MethodGet, "/admin/keys/"+other.ID+"/offered-models?client=Codex", "", c)
	if id, serving := head(offered); id != "claude-large" || serving != "claude-haiku-4-5" {
		t.Errorf("offered to Codex: %q serving %q", id, serving)
	}
	_, offered = call(t, base, http.MethodGet, "/admin/keys/"+other.ID+"/offered-models", "", c)
	if _, serving := head(offered); serving != "claude-opus-5" {
		t.Errorf("offered to no client in particular: serving %q", serving)
	}

	// The admin view: the client's binding under its alias, and the clients
	// one could be made for, the known ones first.
	_, body := call(t, base, http.MethodGet, "/admin/model-roles", "", c)
	var view struct {
		Roles []struct {
			Role    string `json:"role"`
			Model   string `json:"model"`
			Clients []struct {
				Client  string `json:"client"`
				Model   string `json:"model"`
				Serving string `json:"serving"`
			} `json:"clients"`
		} `json:"roles"`
		Clients []string `json:"clients"`
	}
	if err := json.Unmarshal(body, &view); err != nil || len(view.Roles) == 0 {
		t.Fatalf("roles: %v %s", err, body)
	}
	large := view.Roles[0]
	if large.Role != "claude-large" || large.Model != "claude-opus-5" || len(large.Clients) != 1 ||
		large.Clients[0].Client != "Codex" || large.Clients[0].Serving != "claude-haiku-4-5" {
		t.Errorf("claude-large in the admin view: %+v", large)
	}
	if got := strings.Join(view.Clients, ","); !strings.HasPrefix(got, "Claude Code,Codex,crush,opencode") {
		t.Errorf("bindable clients = %q", got)
	}
	_, body = call(t, base, http.MethodGet, "/admin/model-roles?key="+laptop.ID, "", c)
	if strings.Contains(string(body), `"clients"`) {
		t.Errorf("a key's view carries client bindings: %s", body)
	}

	for _, bad := range []map[string]string{
		{"key_id": laptop.ID, "client": "Codex", "role": "claude-large", "model": "claude-opus-5"},
		{"client": "bad\tname", "role": "claude-large", "model": "claude-opus-5"},
		{"client": "Codex", "role": "never-made", "model": "claude-opus-5"},
		{"client": "Codex", "role": "claude-large", "model": "claude-small"},
	} {
		if status, body := put(bad); status != http.StatusBadRequest {
			t.Errorf("PUT %v: %d %s", bad, status, body)
		}
	}

	// Codex's model switched off: refused, nothing sent, naming Codex's
	// binding as the one that chose it.
	b, _ := json.Marshal(map[string]any{"model": "claude-haiku-4-5-20251001", "enabled": false})
	if status, body := call(t, base, http.MethodPost, "/admin/accounts/"+acct.ID+"/models", string(b), c); status != http.StatusOK {
		t.Fatalf("switch off: %d %s", status, body)
	}
	before := sentCount()
	if status, _, body := ask(otherKey, codexUA); status != http.StatusNotFound || !strings.Contains(body, "for Codex") ||
		sentCount() != before {
		t.Errorf("Codex's binding that cannot serve: %d %s", status, body)
	}

	// Cleared, Codex follows the gateway again.
	bind(map[string]string{"client": "Codex", "role": "claude-large", "model": ""})
	if _, sent, _ := ask(otherKey, codexUA); sent != "claude-opus-5" {
		t.Errorf("Codex after clearing its binding: sent %q", sent)
	}
}
