package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"claudication/internal/store"
)

// A cut of models.dev's api.json as measured: other providers beside
// Anthropic, and one model listed under its alias and its dated id both.
const catalogFixture = `{
	"openai": {"models": {"gpt-5": {"id": "gpt-5", "name": "GPT-5", "release_date": "2026-01-01",
		"limit": {"context": 400000, "output": 128000}}}},
	"anthropic": {"id": "anthropic", "models": {
		"claude-haiku-4-5": {"id": "claude-haiku-4-5", "name": "Claude Haiku 4.5",
			"release_date": "2025-10-01", "limit": {"context": 200000, "output": 64000}},
		"claude-haiku-4-5-20251001": {"id": "claude-haiku-4-5-20251001", "name": "Claude Haiku 4.5",
			"release_date": "2025-10-01", "limit": {"context": 200000, "output": 64000}},
		"claude-opus-5": {"id": "claude-opus-5", "name": "Claude Opus 5",
			"release_date": "2026-02-05", "limit": {"context": 1000000, "output": 128000}},
		"claude-opus-5-5": {"id": "claude-opus-5-5", "name": "Claude Opus 5.5",
			"release_date": "2026-09-01", "limit": {"context": 1000000, "output": 128000}}
	}}
}`

// With no account, the model list is the catalogue's — and the aliases with
// it, as they will serve once one is connected — and the catalogue is read
// once, not once per list. With an account, the account's list is what is
// listed, with what it leaves out filled in from the catalogue.
func TestModelCatalogue(t *testing.T) {
	srv, st, _ := newTestServer(t)
	var reads atomic.Int32
	cat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		_, _ = io.WriteString(w, catalogFixture)
	}))
	defer cat.Close()
	srv.cfg.ModelsCatalog = cat.URL

	_, key, err := st.CreateKey(context.Background(), "laptop", store.KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	base, _ := adminAPI(t, srv)
	type entry struct {
		ID        string `json:"id"`
		Serving   string `json:"serving"`
		MaxInput  int    `json:"max_input_tokens"`
		MaxOutput int    `json:"max_tokens"`
		CreatedAt string `json:"created_at"`
	}
	list := func() []entry {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
		req.Header.Set("X-Api-Key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("/v1/models: %d %s", resp.StatusCode, b)
		}
		var l struct {
			Data []entry `json:"data"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&l)
		return l.Data
	}

	got := list()
	ids := []string{}
	for _, e := range got {
		ids = append(ids, e.ID)
	}
	want := []string{"claude-large", "claude-small", "claude-opus", "claude-haiku",
		"claude-opus-5-5", "claude-opus-5", "claude-haiku-4-5"}
	if len(ids) != len(want) {
		t.Fatalf("no account: listed %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("no account: listed %v, want %v", ids, want)
		}
	}
	if got[0].Serving != "claude-opus-5-5" || got[0].MaxInput != 1000000 || got[0].MaxOutput != 128000 {
		t.Errorf("claude-large from the catalogue: %+v", got[0])
	}
	if got[4].CreatedAt != "2026-09-01T00:00:00Z" {
		t.Errorf("release date: %q", got[4].CreatedAt)
	}
	_ = list()
	if n := reads.Load(); n != 1 {
		t.Errorf("catalogue read %d times for two lists", n)
	}

	// An account connected: its list, filled in, and nothing the catalogue
	// has that the account does not.
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
	byID := map[string]entry{}
	for _, e := range list() {
		byID[e.ID] = e
	}
	if _, ok := byID["claude-haiku-4-5"]; ok {
		t.Error("a model only the catalogue has is listed beside an account's list")
	}
	// The account's own figure stands; a missing one is filled, by the
	// undated id for a dated one.
	if e := byID["claude-opus-5"]; e.MaxInput != 1000000 || e.MaxOutput != 128000 {
		t.Errorf("claude-opus-5 not filled in: %+v", e)
	}
	if e := byID["claude-haiku-4-5-20251001"]; e.MaxInput != 200000 || e.CreatedAt != "2025-10-01T00:00:00Z" {
		t.Errorf("dated id not filled in: %+v", e)
	}
}
