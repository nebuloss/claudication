package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"claudication/internal/store"
)

// A model kept off a key is refused for that key before anything is spent,
// stays usable by every other key, drops out of that key's /v1/models, and is
// back the moment it is switched on — under either of its names.
func TestKeyModelSwitchesOverTheAPI(t *testing.T) {
	srv, st, _ := newTestServer(t)

	var upstreamCalls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m",
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

	addAccount(t, srv, st, "first@example.com", "first-access", "r1", time.Now().Add(time.Hour))
	script, scriptKey, err := st.CreateKey(context.Background(), "script", store.KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	_, laptopKey, err := st.CreateKey(context.Background(), "laptop", store.KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	base, c := adminAPI(t, srv)

	set := func(model string, on bool) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"model": model, "enabled": on})
		if status, body := call(t, base, http.MethodPost, "/admin/keys/"+script.ID+"/models", string(b), c); status != http.StatusOK {
			t.Fatalf("switch %s: %d %s", model, status, body)
		}
	}
	ask := func(key, model string) (int, []byte) {
		t.Helper()
		resp := post(t, base+"/v1/messages", key,
			`{"model":"`+model+`","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}
	listed := func(key string) []string {
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

	// Off for the script key, switched by its dated id, asked for by its alias.
	set("claude-haiku-4-5-20251001", false)
	before := upstreamCalls.Load()
	status, body := ask(scriptKey, "claude-haiku-4-5")
	if status != http.StatusForbidden || errType(t, body) != "permission_error" ||
		!strings.Contains(string(body), "may not use model") {
		t.Errorf("haiku for the script key: %d %s", status, body)
	}
	if upstreamCalls.Load() != before {
		t.Error("a refused model reached the upstream")
	}
	if status, body := ask(laptopKey, "claude-haiku-4-5"); status != http.StatusOK {
		t.Errorf("haiku for another key: %d %s", status, body)
	}
	if status, _ := ask(scriptKey, "claude-opus-5"); status != http.StatusOK {
		t.Errorf("another model for the script key: %d", status)
	}

	// Its menu leaves the model out; every other key's still has it.
	if ids := listed(scriptKey); !slices.Equal(ids, []string{"claude-opus-5-5", "claude-opus-5"}) {
		t.Errorf("script key's /v1/models = %v", ids)
	}
	if ids := listed(laptopKey); len(ids) != 3 {
		t.Errorf("laptop key's /v1/models = %v, want all three", ids)
	}

	// The key's own list, and the keys screen, say which are off.
	status, body = call(t, base, http.MethodGet, "/admin/keys/"+script.ID+"/models", "", c)
	if status != http.StatusOK {
		t.Fatalf("key models: %d %s", status, body)
	}
	got := map[string]bool{}
	for _, m := range decode[struct {
		Models []struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
		} `json:"models"`
	}](t, body).Models {
		got[m.ID] = m.Enabled
	}
	if got["claude-haiku-4-5-20251001"] || !got["claude-opus-5"] {
		t.Errorf("script key's models = %v", got)
	}
	_, body = call(t, base, http.MethodGet, "/admin/keys", "", c)
	for _, k := range decode[struct {
		Keys []struct {
			ID        string   `json:"id"`
			ModelsOff []string `json:"models_off"`
		} `json:"keys"`
	}](t, body).Keys {
		if want := k.ID == script.ID; slices.Contains(k.ModelsOff, "claude-haiku-4-5-20251001") != want {
			t.Errorf("%s models_off = %v", k.ID, k.ModelsOff)
		}
	}

	// The refusal is in the request log, under the key.
	rows, _, err := st.RecentUsage(context.Background(), 50, store.UsageCursor{}, store.RequestFilter{KeyIDs: []string{script.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(rows, func(e store.UsageEvent) bool { return e.Rejected && e.Status == http.StatusForbidden }) {
		t.Error("the refusal was not recorded under the key")
	}

	// On again by its alias: allowed.
	set("claude-haiku-4-5", true)
	if status, _ := ask(scriptKey, "claude-haiku-4-5"); status != http.StatusOK {
		t.Errorf("haiku back on: %d", status)
	}

	if status, _ := call(t, base, http.MethodGet, "/admin/keys/nope/models", "", c); status != http.StatusNotFound {
		t.Errorf("models of an unknown key: %d", status)
	}
	if status, _ := call(t, base, http.MethodPost, "/admin/keys/nope/models", `{"model":"m","enabled":false}`, c); status != http.StatusNotFound {
		t.Errorf("switch on an unknown key: %d", status)
	}
}
