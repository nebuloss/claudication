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

// A virtual model, end to end: the upstream is asked for the real model it
// maps to, the client is answered in the name it asked for, the most specific
// mapping wins — this key's Claude Code over this key over the gateway — a
// target that is off falls through to the next, and the model list each app
// is offered says the same.
func TestVirtualModelsOverTheAPI(t *testing.T) {
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

	put := func(keyID, client, name string, targets ...string) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{
			"key_id": keyID, "client": client, "name": name,
			"display_name": "Smart", "targets": targets,
		})
		if status, body := call(t, base, http.MethodPut, "/admin/virtual-models", string(b), c); status != http.StatusOK {
			t.Fatalf("put %s/%s/%s: %d %s", keyID, client, name, status, body)
		}
	}
	ask := func(key, userAgent, model string) (int, string, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, base+"/v1/messages",
			strings.NewReader(`{"model":"`+model+`","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("X-Api-Key", key)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", userAgent)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
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

	const claudeCode, codex = "claude-cli/2.1.274 (external, sdk-cli)", "codex_cli_rs/0.54.0"

	put("", "", "claude-smart", "claude-opus-5-5", "claude-opus-5")
	put(laptop.ID, "", "claude-smart", "claude-opus-5")
	put(laptop.ID, "Claude Code", "claude-smart", "claude-haiku-4-5")

	for _, c := range []struct {
		name, key, ua, sent string
	}{
		{"gateway-wide for another key", otherKey, claudeCode, "claude-opus-5-5"},
		{"the key's own for its Codex", laptopKey, codex, "claude-opus-5"},
		{"the key's Claude Code's own", laptopKey, claudeCode, "claude-haiku-4-5"},
	} {
		status, sent, answered := ask(c.key, c.ua, "claude-smart")
		if status != http.StatusOK || sent != c.sent || answered != "claude-smart" {
			t.Errorf("%s: %d, upstream asked for %q, answer named %q", c.name, status, sent, answered)
		}
	}

	// Its first target off on every account: the next one serves.
	b, _ := json.Marshal(map[string]any{"model": "claude-opus-5-5", "enabled": false})
	if status, body := call(t, base, http.MethodPost, "/admin/accounts/"+acct.ID+"/models", string(b), c); status != http.StatusOK {
		t.Fatalf("switch off: %d %s", status, body)
	}
	if status, sent, _ := ask(otherKey, claudeCode, "claude-smart"); status != http.StatusOK || sent != "claude-opus-5" {
		t.Errorf("fallback: %d, sent %q", status, sent)
	}

	// Every target unavailable: refused before anything is sent, saying why.
	put("", "", "claude-gone", "claude-opus-5-5")
	before := len(models)
	if status, _, _ := ask(otherKey, claudeCode, "claude-gone"); status != http.StatusNotFound || len(models) != before {
		t.Errorf("nothing to serve it: %d, upstream asked %d times", status, len(models)-before)
	}

	// The log records both names.
	rows, _, err := st.RecentUsage(context.Background(), 50, store.UsageCursor{}, store.RequestFilter{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range rows {
		if e.VirtualModel == "claude-smart" && e.Model == "claude-haiku-4-5" {
			found = true
		}
	}
	if !found {
		t.Error("no request recorded as claude-smart served by claude-haiku-4-5")
	}

	// What each app is offered: the virtual model first, as it serves there.
	listed := func(key, ua string) (string, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
		req.Header.Set("X-Api-Key", key)
		req.Header.Set("User-Agent", ua)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var l struct {
			Data []struct {
				ID      string `json:"id"`
				Serving string `json:"serving"`
			} `json:"data"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&l)
		if len(l.Data) == 0 {
			return "", ""
		}
		return l.Data[0].ID, l.Data[0].Serving
	}
	if id, serving := listed(laptopKey, claudeCode); id != "claude-smart" || serving != "claude-haiku-4-5" {
		t.Errorf("laptop's Claude Code is offered %s serving %s", id, serving)
	}
	if id, serving := listed(otherKey, codex); id != "claude-smart" || serving != "claude-opus-5" {
		t.Errorf("another key is offered %s serving %s", id, serving)
	}

	// Removing the app's mapping lets the key's apply again.
	if status, body := call(t, base, http.MethodDelete,
		"/admin/virtual-models?key_id="+laptop.ID+"&client=Claude+Code&name=claude-smart", "", c); status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", status, body)
	}
	if _, sent, _ := ask(laptopKey, claudeCode, "claude-smart"); sent != "claude-opus-5" {
		t.Errorf("after removing the app's mapping, sent %q", sent)
	}

	// Validation: a client mapping belongs to a key; a name is a model id.
	for _, bad := range []string{
		`{"client":"Codex","name":"claude-x","targets":["claude-opus-5"]}`,
		`{"name":"Bad Name","targets":["claude-opus-5"]}`,
		`{"name":"claude-x","targets":[]}`,
	} {
		if status, _ := call(t, base, http.MethodPut, "/admin/virtual-models", bad, c); status != http.StatusBadRequest {
			t.Errorf("PUT %s: %d", bad, status)
		}
	}
}
