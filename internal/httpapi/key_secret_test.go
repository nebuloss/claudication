package httpapi

import (
	"context"
	"net/http"
	"testing"
)

// A key made through the admin API can be shown again — the same secret, which
// authenticates — and after a new secret is issued, it is the new one that is
// shown.
func TestKeySecretCanBeShownAgain(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, c := adminAPI(t, srv)

	type minted struct {
		Key struct {
			ID         string `json:"id"`
			SecretKept bool   `json:"secret_kept"`
		} `json:"client"`
		Plaintext string `json:"plaintext"`
	}
	status, body := call(t, base, http.MethodPost, "/admin/clients", `{"name":"laptop","type":"crush"}`, c)
	if status != http.StatusOK {
		t.Fatalf("create: %d %s", status, body)
	}
	made := decode[minted](t, body)
	if !made.Key.SecretKept {
		t.Error("a new key does not report its secret kept")
	}

	shown := func(id string) (int, string) {
		t.Helper()
		status, body := call(t, base, http.MethodGet, "/admin/keys/"+id+"/secret", "", c)
		if status != http.StatusOK {
			return status, errType(t, body)
		}
		return status, decode[struct {
			Plaintext string `json:"plaintext"`
		}](t, body).Plaintext
	}
	if status, got := shown(made.Key.ID); status != http.StatusOK || got != made.Plaintext {
		t.Errorf("shown again: %d %q, want the secret issued", status, got)
	}

	status, body = call(t, base, http.MethodPost, "/admin/keys/"+made.Key.ID+"/rotate", "", c)
	if status != http.StatusOK {
		t.Fatalf("rotate: %d %s", status, body)
	}
	rotated := decode[minted](t, body)
	if status, got := shown(made.Key.ID); status != http.StatusOK || got != rotated.Plaintext || got == made.Plaintext {
		t.Errorf("after a new secret: %d %q", status, got)
	}
	if _, err := st.Authenticate(context.Background(), rotated.Plaintext); err != nil {
		t.Errorf("the shown secret does not authenticate: %v", err)
	}

	if status, _ := shown("nope"); status != http.StatusNotFound {
		t.Errorf("an unknown key: %d", status)
	}
}
