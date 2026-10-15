package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"claudication/internal/store"
)

// The installer end to end over HTTP: the admin API keeps a key's files, the
// install address serves them as a script, and every way of asking wrongly is
// a status a pipe can refuse on, with a script that says why.
func TestInstallAddress(t *testing.T) {
	srv, st, _ := newTestServer(t)
	key, _, err := st.CreateKey(context.Background(), "laptop", store.KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	base, c := adminAPI(t, srv)

	files := `{"files":[
		{"client_id":"codex","client":"Codex CLI","path":".codex/config.toml","body":"model = \"claude-large\"\n"},
		{"client_id":"crush","client":"crush","path":".config/crush/crush.json","windows_path":"AppData/Local/crush/crush.json","body":"{}\n"}]}`
	status, body := call(t, base, http.MethodPost, "/admin/keys/"+key.ID+"/install", files, c)
	if status != http.StatusOK {
		t.Fatalf("make a link: %d %s", status, body)
	}
	var made struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(body, &made)

	get := func(query string) (int, string) {
		t.Helper()
		resp, err := http.Get(base + "/install?" + query)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	if status, script := get("token=" + made.Token + "&shell=sh"); status != http.StatusOK ||
		!strings.Contains(script, ".codex/config.toml") || !strings.Contains(script, ".config/crush/crush.json") {
		t.Errorf("every client: %d\n%s", status, script)
	}
	if status, script := get("token=" + made.Token + "&shell=sh&client=crush"); status != http.StatusOK ||
		strings.Contains(script, ".codex/") {
		t.Errorf("client=crush: %d\n%s", status, script)
	}
	if status, script := get("token=" + made.Token + "&shell=ps1"); status != http.StatusOK ||
		!strings.Contains(script, `AppData\Local\crush\crush.json`) {
		t.Errorf("ps1: %d\n%s", status, script)
	}

	for _, c := range []struct {
		query  string
		status int
	}{
		{"shell=sh", http.StatusUnauthorized},
		{"token=nope&shell=sh", http.StatusUnauthorized},
		{"token=" + made.Token + "&shell=bash", http.StatusBadRequest},
		{"token=" + made.Token + "&shell=sh&client=nope", http.StatusBadRequest},
	} {
		status, script := get(c.query)
		if status != c.status || !strings.Contains(script, "exit 1") {
			t.Errorf("%s: %d, want %d\n%s", c.query, status, c.status, script)
		}
	}
	if status, script := get("token=nope&shell=ps1"); status != http.StatusUnauthorized || !strings.HasPrefix(script, "Write-Error") {
		t.Errorf("ps1 failure: %d %s", status, script)
	}

	// Files that would land outside the home directory are refused at once.
	bad := `{"files":[{"client_id":"x","client":"x","path":"../etc/passwd","body":"x"}]}`
	if status, body := call(t, base, http.MethodPost, "/admin/keys/"+key.ID+"/install", bad, c); status != http.StatusBadRequest {
		t.Errorf("a path out of home: %d %s", status, body)
	}
	if status, _ := call(t, base, http.MethodPost, "/admin/keys/nope/install", files, c); status != http.StatusNotFound {
		t.Errorf("an unknown key: %d", status)
	}
}
