package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"claudication/internal/httpapi/admin"
)

// A client made over the admin API holds a key that works, is listed without
// its secret, and stops working the moment it is deleted.
func TestClientLifecycleOverTheAdminAPI(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()
	setupAdmin(t, st)

	in := postJSON(t, base, http.MethodPost, "/admin/session",
		map[string]string{"password": testPassword})
	cookie := sessionCookieOf(t, in)
	in.Body.Close()

	// Create.
	made := postJSON(t, base, http.MethodPost, "/admin/clients",
		map[string]any{"name": "laptop", "type": "crush", "rpm_limit": 120}, cookie)
	if made.StatusCode != http.StatusOK {
		t.Fatalf("create: status = %d, want 200", made.StatusCode)
	}
	var created struct {
		Client    admin.ClientJSON `json:"client"`
		Plaintext string           `json:"plaintext"`
	}
	if err := json.NewDecoder(made.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	made.Body.Close()

	if !strings.HasPrefix(created.Plaintext, "clc_") {
		t.Errorf("plaintext = %q, want a clc_ prefix", created.Plaintext)
	}
	// The whole point of hashing it: the listing must not be able to show it.
	if strings.Contains(created.Client.Display, created.Plaintext) {
		t.Error("the display form must be a prefix, not the key")
	}
	c := created.Client
	if c.RPMLimit != 120 || c.Type != "crush" || c.Mode != "binding" ||
		len(c.Slots) != 2 || c.Slots[0].Name != "large" || c.Slots[1].Name != "small" ||
		c.Slots[0].Bound != "claude:opus" {
		t.Errorf("created %+v", c)
	}
	// A client keeps the type it was made with: its key was issued for it,
	// so there is no endpoint that changes it.
	for _, p := range []string{"/plan", "/type"} {
		r := postJSON(t, base, http.MethodPost, "/admin/clients/"+c.ID+p,
			map[string]any{"type": "codex"}, cookie)
		r.Body.Close()
		if r.StatusCode == http.StatusOK {
			t.Errorf("POST %s changed a client", p)
		}
	}
	if got, err := st.Client(context.Background(), c.ID); err != nil || got.Type != "crush" {
		t.Errorf("after the attempts: %+v %v", got, err)
	}

	// The key works against the proxy surface. What is under test here is the
	// credential, not discovery: with no upstream account connected the
	// request cannot be served, and the point is that it fails at the upstream
	// rather than at the door.
	req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
	req.Header.Set("X-Api-Key", created.Plaintext)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		t.Fatalf("new key against /v1/models: status = %d, want the key to be accepted", resp.StatusCode)
	}

	// List.
	var listed struct {
		Clients []admin.ClientJSON `json:"clients"`
	}
	list := getWithCookie(t, base, "/admin/clients", cookie)
	if err := json.NewDecoder(list.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	list.Body.Close()
	if len(listed.Clients) != 1 || listed.Clients[0].Name != "laptop" {
		t.Fatalf("listing = %+v, want one client named laptop", listed.Clients)
	}
	// A listing that leaked the plaintext would undo the hashing entirely.
	if strings.Contains(listed.Clients[0].Display, strings.TrimPrefix(created.Plaintext, "clc_")) {
		t.Error("the listing leaks the full key")
	}

	// Delete, and the credential stops working immediately.
	del := postJSON(t, base, http.MethodDelete, "/admin/keys/"+c.ID, nil, cookie)
	del.Body.Close()
	if del.StatusCode != http.StatusOK {
		t.Fatalf("delete: status = %d, want 200", del.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodGet, base+"/v1/models", nil)
	req.Header.Set("X-Api-Key", created.Plaintext)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("deleted key: status = %d, want 401", resp.StatusCode)
	}
	again := postJSON(t, base, http.MethodDelete, "/admin/keys/"+c.ID, nil, cookie)
	again.Body.Close()
	if again.StatusCode != http.StatusNotFound {
		t.Errorf("second delete: status = %d, want 404", again.StatusCode)
	}
	list = getWithCookie(t, base, "/admin/clients", cookie)
	listed.Clients = nil
	if err := json.NewDecoder(list.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	list.Body.Close()
	if len(listed.Clients) != 0 {
		t.Errorf("after delete the listing still has %d clients", len(listed.Clients))
	}
}

func TestCreateClientRefusesWhatCannotBe(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()
	setupAdmin(t, st)

	in := postJSON(t, base, http.MethodPost, "/admin/session",
		map[string]string{"password": testPassword})
	cookie := sessionCookieOf(t, in)
	in.Body.Close()

	for _, body := range []map[string]any{
		{"name": "", "type": "crush"},
		{"name": "   ", "type": "crush"},
		{"name": "x", "type": "emacs"},
	} {

		resp := postJSON(t, base, http.MethodPost, "/admin/clients", body, cookie)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%v: status = %d, want 400", body, resp.StatusCode)
		}
	}
}

// Every one of these reads or changes gateway state, so none of them may be
// reachable without a session.
func TestNewAdminEndpointsRequireASession(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()
	setupAdmin(t, st)

	for _, path := range []string{"/admin/overview", "/admin/clients", "/admin/usage", "/admin/usage/slots", "/admin/usage/live", "/admin/requests"} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s: status = %d, want 401", path, resp.StatusCode)
		}
	}

	resp := postJSON(t, base, http.MethodPost, "/admin/clients", map[string]any{"name": "x", "type": "crush"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /admin/clients: status = %d, want 401", resp.StatusCode)
	}
}
