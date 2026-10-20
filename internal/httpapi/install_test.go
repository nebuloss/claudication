package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// The installer end to end over HTTP: the admin API keeps a client's files,
// the install address serves them as a script once, the client's page sees
// the link waiting and then used, and from then on neither the key nor a new
// link is offered — until the key is regenerated. Every way of asking wrongly
// is a status a pipe can refuse on, with a script that says why.
func TestInstallAddress(t *testing.T) {
	srv, st, _ := newTestServer(t)
	c, _ := newClient(t, st, "laptop", "crush", "", nil)
	base, ck := adminAPI(t, srv)

	files := `{"files":[
		{"client_id":"crush","client":"crush","path":".config/crush/crush.json","windows_path":"AppData/Local/crush/crush.json","body":"{}\n"}]}`
	link := func() string {
		t.Helper()
		status, body := call(t, base, http.MethodPost, "/admin/keys/"+c.ID+"/install", files, ck)
		if status != http.StatusOK {
			t.Fatalf("make a link: %d %s", status, body)
		}
		var made struct {
			Token string `json:"token"`
		}
		_ = json.Unmarshal(body, &made)
		return made.Token
	}
	type state struct {
		Client struct {
			ConfiguredAt string `json:"configured_at"`
			Install      struct {
				State string `json:"state"`
				Used  *struct {
					IP    string `json:"ip"`
					Shell string `json:"shell"`
				} `json:"used"`
			} `json:"install"`
		} `json:"client"`
	}
	page := func() state {
		t.Helper()
		_, body := call(t, base, http.MethodGet, "/admin/clients/"+c.ID, "", ck)
		return decode[state](t, body)
	}
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

	if s := page().Client.Install.State; s != "none" {
		t.Errorf("before a link: %q", s)
	}
	token := link()
	if p := page(); p.Client.Install.State != "waiting" || p.Client.ConfiguredAt != "" {
		t.Errorf("made: %+v", p)
	}
	for _, q := range []struct {
		query  string
		status int
	}{
		{"shell=sh", http.StatusUnauthorized},
		{"token=nope&shell=sh", http.StatusUnauthorized},
		{"token=" + token + "&shell=bash", http.StatusBadRequest},
	} {
		status, script := get(q.query)
		if status != q.status || !strings.Contains(script, "exit 1") {
			t.Errorf("%s: %d, want %d\n%s", q.query, status, q.status, script)
		}
	}
	if status, script := get("token=" + token + "&shell=ps1"); status != http.StatusOK ||
		!strings.Contains(script, `AppData\Local\crush\crush.json`) {
		t.Fatalf("ps1: %d\n%s", status, script)
	}
	// Single use: the second fetch is told so, and the page sees it used.
	if status, script := get("token=" + token + "&shell=sh"); status != http.StatusGone ||
		!strings.Contains(script, "already used") {
		t.Errorf("a second fetch: %d\n%s", status, script)
	}
	p := page()
	if p.Client.Install.State != "used" || p.Client.Install.Used == nil || p.Client.Install.Used.Shell != "ps1" ||
		p.Client.ConfiguredAt == "" {
		t.Errorf("after the fetch: %+v", p)
	}
	// Configured: neither the key nor another link is offered.
	if status, body := call(t, base, http.MethodGet, "/admin/keys/"+c.ID+"/secret", "", ck); status != http.StatusConflict ||
		!strings.Contains(string(body), "configured") {
		t.Errorf("secret of a configured client: %d %s", status, body)
	}
	if status, _ := call(t, base, http.MethodPost, "/admin/keys/"+c.ID+"/install", files, ck); status != http.StatusConflict {
		t.Errorf("a link for a configured client: %d", status)
	}
	// A new key is a client to set up again.
	if status, body := call(t, base, http.MethodPost, "/admin/keys/"+c.ID+"/rotate", "", ck); status != http.StatusOK {
		t.Fatalf("rotate: %d %s", status, body)
	}
	if p := page(); p.Client.ConfiguredAt != "" || p.Client.Install.State != "none" {
		t.Errorf("after a new key: %+v", p)
	}
	if status, _ := call(t, base, http.MethodGet, "/admin/keys/"+c.ID+"/secret", "", ck); status != http.StatusOK {
		t.Errorf("the new key is not offered: %d", status)
	}
	if status, script := get("token=" + link() + "&shell=sh"); status != http.StatusOK || !strings.Contains(script, ".config/crush/crush.json") {
		t.Errorf("sh after a new key: %d\n%s", status, script)
	}

	// Files that would land outside the home directory are refused at once.
	if status, body := call(t, base, http.MethodPost, "/admin/keys/"+c.ID+"/rotate", "", ck); status != http.StatusOK {
		t.Fatalf("rotate: %d %s", status, body)
	}
	bad := `{"files":[{"client_id":"x","client":"x","path":"../etc/passwd","body":"x"}]}`
	if status, body := call(t, base, http.MethodPost, "/admin/keys/"+c.ID+"/install", bad, ck); status != http.StatusBadRequest {
		t.Errorf("a path out of home: %d %s", status, body)
	}
	if status, _ := call(t, base, http.MethodPost, "/admin/keys/nope/install", files, ck); status != http.StatusNotFound {
		t.Errorf("an unknown key: %d", status)
	}
}

// A client set up by hand is configured by its first request, refused or
// not, and its key stops being offered.
func TestFirstRequestConfiguresTheClient(t *testing.T) {
	srv, st, _ := newTestServer(t)
	c, plain := newClient(t, st, "box", "crush", "", nil)
	if err := st.SetClientDisabled(t.Context(), c.ID, true); err != nil {
		t.Fatal(err)
	}
	base, ck := adminAPI(t, srv)
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/messages", strings.NewReader(`{"model":"large"}`))
	req.Header.Set("X-Api-Key", plain)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a disabled client was served: %d", resp.StatusCode)
	}
	got, err := st.Client(t.Context(), c.ID)
	if err != nil || got.ConfiguredAt == nil {
		t.Errorf("not configured by its first request: %+v, %v", got.ConfiguredAt, err)
	}
	if status, _ := call(t, base, http.MethodGet, "/admin/keys/"+c.ID+"/secret", "", ck); status != http.StatusConflict {
		t.Errorf("secret after first use: %d", status)
	}
}
