package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestKeyLifecycleOverTheAdminAPI(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()
	setupAdmin(t, st)

	in := postJSON(t, base, http.MethodPost, "/admin/session",
		map[string]string{"password": testPassword})
	cookie := sessionCookieOf(t, in)
	in.Body.Close()

	// Create.
	made := postJSON(t, base, http.MethodPost, "/admin/keys",
		map[string]any{"name": "laptop", "rpm_limit": 120}, cookie)
	if made.StatusCode != http.StatusOK {
		t.Fatalf("create: status = %d, want 200", made.StatusCode)
	}
	var created struct {
		Key       keyJSON `json:"key"`
		Plaintext string  `json:"plaintext"`
	}
	if err := json.NewDecoder(made.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	made.Body.Close()

	if !strings.HasPrefix(created.Plaintext, "clc_") {
		t.Errorf("plaintext = %q, want a clc_ prefix", created.Plaintext)
	}
	// The whole point of hashing it: the listing must not be able to show it.
	if strings.Contains(created.Key.Display, created.Plaintext) {
		t.Error("the display form must be a prefix, not the key")
	}
	if created.Key.RPMLimit != 120 {
		t.Errorf("rpm_limit = %d, want 120", created.Key.RPMLimit)
	}

	// The key works against the proxy surface.
	req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
	req.Header.Set("X-Api-Key", created.Plaintext)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("new key against /v1/models: status = %d, want 200", resp.StatusCode)
	}

	// List.
	list := getWithCookie(t, base, "/admin/keys", cookie)
	var listed struct {
		Keys []keyJSON `json:"keys"`
	}
	if err := json.NewDecoder(list.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	list.Body.Close()
	if len(listed.Keys) != 1 || listed.Keys[0].Name != "laptop" {
		t.Fatalf("listing = %+v, want one key named laptop", listed.Keys)
	}
	// A listing that leaked the plaintext would undo the hashing entirely.
	if strings.Contains(listed.Keys[0].Display, strings.TrimPrefix(created.Plaintext, "clc_")) {
		t.Error("the listing leaks the full key")
	}

	// Revoke, and the credential stops working immediately.
	rev := postJSON(t, base, http.MethodPost, "/admin/keys/"+created.Key.ID+"/revoke", nil, cookie)
	rev.Body.Close()
	if rev.StatusCode != http.StatusOK {
		t.Fatalf("revoke: status = %d, want 200", rev.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodGet, base+"/v1/models", nil)
	req.Header.Set("X-Api-Key", created.Plaintext)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("revoked key: status = %d, want 401", resp.StatusCode)
	}

	// Revoking twice is not an error the operator caused; it is a 404 because
	// there is nothing left to revoke.
	again := postJSON(t, base, http.MethodPost, "/admin/keys/"+created.Key.ID+"/revoke", nil, cookie)
	again.Body.Close()
	if again.StatusCode != http.StatusNotFound {
		t.Errorf("second revoke: status = %d, want 404", again.StatusCode)
	}

	// Delete.
	del := postJSON(t, base, http.MethodDelete, "/admin/keys/"+created.Key.ID, nil, cookie)
	del.Body.Close()
	if del.StatusCode != http.StatusOK {
		t.Fatalf("delete: status = %d, want 200", del.StatusCode)
	}
	list = getWithCookie(t, base, "/admin/keys", cookie)
	listed.Keys = nil
	if err := json.NewDecoder(list.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	list.Body.Close()
	if len(listed.Keys) != 0 {
		t.Errorf("after delete the listing still has %d keys", len(listed.Keys))
	}
}

func TestCreateKeyRejectsAnEmptyName(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()
	setupAdmin(t, st)

	in := postJSON(t, base, http.MethodPost, "/admin/session",
		map[string]string{"password": testPassword})
	cookie := sessionCookieOf(t, in)
	in.Body.Close()

	for _, name := range []string{"", "   "} {
		resp := postJSON(t, base, http.MethodPost, "/admin/keys",
			map[string]any{"name": name}, cookie)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("name %q: status = %d, want 400", name, resp.StatusCode)
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

	for _, path := range []string{"/admin/overview", "/admin/keys", "/admin/usage", "/admin/requests"} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s: status = %d, want 401", path, resp.StatusCode)
		}
	}

	resp := postJSON(t, base, http.MethodPost, "/admin/keys", map[string]any{"name": "x"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /admin/keys: status = %d, want 401", resp.StatusCode)
	}
}
