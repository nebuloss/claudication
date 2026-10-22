package httpapi

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"claudication/internal/httpapi/admin"
	"claudication/internal/store"
)

// A model switched off on a client's Models page is out of every path a
// model reaches that client by — the admin menu, list_models, bind_model and
// add_model, /v1/models and the request itself — and so is a target while it
// resolves to it. The owner's point: it bounds what the client's agent may
// switch it to. The switches themselves still list it, to be switched back.
func TestAllowedModelsBoundEveryPath(t *testing.T) {
	srv, st, url := mcpServer(t)
	// Per model: a version's switch is read where every model is offered;
	// TestFamiliesBoundEveryPath has the family's.
	everyModel(t, srv)
	ctx := context.Background()
	abase, ck := adminAPI(t, srv)
	c, plain := newClient(t, st, "box", "crush", map[string]store.Binding{"large": {Model: "claude-sonnet-5"}})
	if err := st.SetKeyModel(ctx, c.ID, "claude-sonnet-5", false); err != nil {
		t.Fatal(err)
	}
	notAllowed := "not allowed for this client"

	// The admin page: out of the menu, its target too, and the name bound
	// to it shown unavailable with why.
	status, body := call(t, abase, http.MethodGet, "/admin/clients/"+c.ID, "", ck)
	if status != http.StatusOK {
		t.Fatalf("get: %d %s", status, body)
	}
	got := decode[struct {
		Client admin.ClientJSON `json:"client"`
	}](t, body).Client
	if v := choiceValues(got.Choices); slices.ContainsFunc(v, func(s string) bool { return strings.Contains(s, "sonnet") }) ||
		!slices.Contains(v, "model:claude-haiku-5") {
		t.Errorf("choices with sonnet not allowed = %v", v)
	}
	if large := got.Slots[0]; large.Unavailable != notAllowed || large.Serving != "claude-sonnet-5" {
		t.Errorf("large, bound to a model not allowed: %+v", large)
	}
	put := func(slot, b string) (int, string) {
		t.Helper()
		status, out := call(t, abase, http.MethodPut, "/admin/clients/"+c.ID+"/slots/"+slot, b, ck)
		return status, string(out)
	}
	for _, b := range []string{`{"model":"claude-sonnet-5"}`, `{"follows":"claude:sonnet"}`} {
		if status, out := put("small", b); status != http.StatusBadRequest || !strings.Contains(out, notAllowed) {
			t.Errorf("binding small to %s: %d %s", b, status, out)
		}
	}
	status, body = call(t, abase, http.MethodPost, "/admin/clients/"+c.ID+"/slots", `{"name":"extra","model":"claude-sonnet-5"}`, ck)
	if status != http.StatusBadRequest || !strings.Contains(string(body), notAllowed) {
		t.Errorf("adding a name bound to it: %d %s", status, body)
	}
	// The switches still list it, off, so it can be switched back on.
	status, body = call(t, abase, http.MethodGet, "/admin/keys/"+c.ID+"/models", "", ck)
	switches := decode[struct {
		Models []struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
		} `json:"models"`
	}](t, body).Models
	if i := slices.IndexFunc(switches, func(m struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}) bool {
		return m.ID == "claude-sonnet-5"
	}); status != http.StatusOK || i < 0 || switches[i].Enabled {
		t.Errorf("the client's switches: %d %+v", status, switches)
	}

	// The request: refused, saying why, and nothing reaches an account.
	resp, out := send(t, url+"/v1/messages", plain, "Charm Crush/0.9",
		`{"model":"large","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(out), notAllowed) {
		t.Errorf("a request for large: %d %s", resp.StatusCode, out)
	}

	// The MCP server: not listed, and not bindable by the agent.
	cl := mcpClient{t: t, url: url, header: "Authorization", value: "Bearer " + plain}
	if _, text, isErr := cl.tool("list_models", `{}`); isErr || strings.Contains(text, "sonnet") ||
		!strings.Contains(text, "claude-haiku-5") {
		t.Errorf("list_models with sonnet not allowed: %s", text)
	}
	if err := st.SetClientAgentManage(ctx, c.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{`{"name":"small","model":"claude-sonnet-5"}`, `{"name":"small","follows":"claude:sonnet"}`} {
		if _, text, isErr := cl.tool("bind_model", args); !isErr || !strings.Contains(text, notAllowed) {
			t.Errorf("bind_model %s: %v %s", args, isErr, text)
		}
	}
	if _, text, isErr := cl.tool("add_model", `{"name":"extra","model":"claude-sonnet-5"}`); !isErr || !strings.Contains(text, notAllowed) {
		t.Errorf("add_model bound to it: %v %s", isErr, text)
	}

	// Allowed again: served again, with nothing rebound.
	if err := st.SetKeyModel(ctx, c.ID, "claude-sonnet-5", true); err != nil {
		t.Fatal(err)
	}
	resp, out = send(t, url+"/v1/messages", plain, "Charm Crush/0.9",
		`{"model":"large","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("large allowed again: %d %s", resp.StatusCode, out)
	}
}
