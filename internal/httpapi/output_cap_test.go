package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"claudication/internal/service/clients"
	"claudication/internal/store"
)

// A request naming a slot has its output budget fitted to the model the slot
// serves now; one naming a real model, from a direct-mode client, keeps its
// own. crush pins max_tokens when a model is picked, so after its slot is
// rebound to a model with a smaller cap the stale number would be refused on
// every request (internal/relay/passes, outputcap.go). The cap is the one the
// account's model list gives, read the first time it is needed.
func TestASlotsBudgetIsFittedToTheModelItServes(t *testing.T) {
	srv, st, _ := newTestServer(t)
	claude := &upstreamStub{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claude.record(r)
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[`+
				`{"type":"model","id":"claude-opus-5-5","created_at":"2026-09-01T00:00:00Z","max_tokens":128000},`+
				`{"type":"model","id":"claude-haiku-5","created_at":"2026-08-01T00:00:00Z","max_tokens":64000}],"has_more":false}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, anthropicStream)
	}))
	t.Cleanup(up.Close)
	srv.relay.BaseURL = up.URL
	if err := seedAccount(t, st, srv); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, crushKey := newClient(t, st, "crush", "crush", map[string]store.Binding{
		"large": {Model: "claude-haiku-5"}, "small": {Model: "claude-haiku-5"}})
	direct, directKey := newClient(t, st, "tool", "custom", nil)
	if err := st.SetClientMode(ctx, direct.ID, clients.Direct); err != nil {
		t.Fatal(err)
	}
	base, cancel, done := startServer(t, srv)
	t.Cleanup(func() { cancel(); <-done })

	sentBudget := func(key, model string, budget int) int {
		t.Helper()
		body := `{"model":"` + model + `","max_tokens":` + strconv.Itoa(budget) +
			`,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
		resp, out := send(t, base+"/v1/messages", key, "Charm-Crush/v0.97.2", body, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d: %s", model, resp.StatusCode, out)
		}
		turns := claude.turns()
		var sent struct {
			MaxTokens int `json:"max_tokens"`
		}
		if err := json.Unmarshal(turns[len(turns)-1].body, &sent); err != nil {
			t.Fatal(err)
		}
		return sent.MaxTokens
	}

	// The budget crush pinned when large served Opus, after large was
	// rebound to Haiku: fitted to Haiku's cap.
	if got := sentBudget(crushKey, "large", 128000); got != 64000 {
		t.Errorf("large at 128000 went up as %d, want Haiku's 64000", got)
	}
	// Under the cap is the client's to choose.
	if got := sentBudget(crushKey, "small", 32000); got != 32000 {
		t.Errorf("small at 32000 went up as %d", got)
	}
	// A real model named by a direct-mode client is the client's own
	// choice: an upstream refusal is the right answer to a budget over it.
	if got := sentBudget(directKey, "claude-haiku-5", 128000); got != 128000 {
		t.Errorf("a direct-mode request went up as %d, want its own 128000", got)
	}
}
