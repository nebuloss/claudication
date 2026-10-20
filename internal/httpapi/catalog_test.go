package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

// With no account, the model list is the catalogue's, read once, not once per
// list — and no client is offered any of it, since none of it can be served.
// With an account, the account's list is what is listed, with what it leaves
// out filled in from the catalogue, and a client's names serve from it.
func TestModelCatalogue(t *testing.T) {
	srv, st, _ := newTestServer(t)
	var reads atomic.Int32
	cat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		_, _ = io.WriteString(w, catalogFixture)
	}))
	defer cat.Close()
	srv.cfg.ModelsCatalog = cat.URL

	_, key := newClient(t, st, "laptop", "crush", "", nil)
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

	// A crush client is offered none of it: with no account nothing can be
	// served, and a client is offered only what can (models/available.go).
	if got := list(); len(got) != 0 {
		t.Errorf("no account: offered %+v", got)
	}
	_ = list()
	if n := reads.Load(); n != 1 {
		t.Errorf("catalogue read %d times for two lists", n)
	}
	// The admin screens still have the catalogue's models to show.
	if body, err := srv.models.List(context.Background()); err != nil || !strings.Contains(string(body), `"claude-opus-5-5"`) {
		t.Errorf("no account: the gateway's list is not the catalogue's: %s %v", body, err)
	}
	ids := []string{}

	// An account connected: the catalogue's models that account lists, in
	// the catalogue's order and words and under the account's ids; one the
	// catalogue does not know yet after them; and nothing the account does
	// not list.
	srv.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(rec, `{"data":[
				{"type":"model","id":"claude-mystery-1","display_name":"Released this morning"},
				{"type":"model","id":"claude-haiku-4-5-20251001","display_name":"haiku"},
				{"type":"model","id":"claude-opus-5-5","display_name":"opus","max_input_tokens":200000}],
				"has_more":false}`)
		} else {
			rec.WriteHeader(http.StatusNotFound)
		}
		return rec.Result(), nil
	})
	addAccount(t, srv, st, "first@example.com", "first-access", "r1", time.Now().Add(time.Hour))
	body, err := srv.models.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var matched struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			MaxInput    int    `json:"max_input_tokens"`
			CreatedAt   string `json:"created_at"`
		} `json:"data"`
		FirstID string `json:"first_id"`
	}
	if err := json.Unmarshal(body, &matched); err != nil {
		t.Fatal(err)
	}
	ids = ids[:0]
	for _, e := range matched.Data {
		ids = append(ids, e.ID)
	}
	if strings.Join(ids, ",") != "claude-opus-5-5,claude-haiku-4-5-20251001,claude-mystery-1" ||
		matched.FirstID != "claude-opus-5-5" {
		t.Fatalf("matched list = %v (first_id %q)", ids, matched.FirstID)
	}
	if e := matched.Data[0]; e.DisplayName != "Claude Opus 5.5" || e.MaxInput != 1000000 {
		t.Errorf("not described by the catalogue: %+v", e)
	}
	if e := matched.Data[1]; e.CreatedAt != "2025-10-01T00:00:00Z" || e.MaxInput != 200000 {
		t.Errorf("dated id not matched to its catalogue entry: %+v", e)
	}
	if e := matched.Data[2]; e.DisplayName != "Released this morning" {
		t.Errorf("a model the catalogue lacks lost the account's description: %+v", e)
	}

	// Now the client is offered its two names, each following the newest of
	// its family the account lists, described as the catalogue has it.
	got := list()
	if len(got) != 2 || got[0].ID != "large" || got[1].ID != "small" {
		t.Fatalf("with an account: offered %+v", got)
	}
	if got[0].Serving != "claude-opus-5-5" || got[0].MaxInput != 1000000 || got[0].MaxOutput != 128000 {
		t.Errorf("large: %+v", got[0])
	}
	if got[1].Serving != "claude-haiku-4-5-20251001" || got[1].MaxInput != 200000 {
		t.Errorf("small: %+v", got[1])
	}
}
