package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// postJSON sends v to path, optionally carrying cookies, and returns the
// response for the caller to close.
func postJSON(t *testing.T, base, method, path string, v any, cookies ...*http.Cookie) *http.Response {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, base+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func sessionCookieOf(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c
		}
	}
	t.Fatal("no session cookie in the response")
	return nil
}

func getWithCookie(t *testing.T, base, path string, c *http.Cookie) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(c)
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// signsIn reports whether password is accepted.
func signsIn(t *testing.T, base, password string) bool {
	t.Helper()
	resp := postJSON(t, base, http.MethodPost, "/admin/session",
		map[string]string{"password": password})
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func TestSetupClaimsTheGatewayOnce(t *testing.T) {
	srv, _, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	// Before setup the UI has to be able to tell a fresh install from a
	// forgotten password, or it shows the wrong screen.
	status, err := http.Get(base + "/admin/setup")
	if err != nil {
		t.Fatal(err)
	}
	var before struct {
		NeedsSetup bool `json:"needs_setup"`
		MinLen     int  `json:"min_password_len"`
	}
	if err := json.NewDecoder(status.Body).Decode(&before); err != nil {
		t.Fatal(err)
	}
	status.Body.Close()
	if !before.NeedsSetup {
		t.Error("needs_setup = false on a fresh gateway")
	}
	if before.MinLen == 0 {
		t.Error("min_password_len must be advertised so the UI can check before posting")
	}

	first := postJSON(t, base, http.MethodPost, "/admin/setup",
		map[string]string{"password": testPassword})
	if first.StatusCode != http.StatusOK {
		t.Fatalf("setup: status = %d, want 200", first.StatusCode)
	}
	cookie := sessionCookieOf(t, first)
	first.Body.Close()

	// The window has to close: an exposed setup endpoint that kept answering
	// would be a way to take a running gateway over.
	second := postJSON(t, base, http.MethodPost, "/admin/setup",
		map[string]string{"password": "someone-elses-password"})
	second.Body.Close()
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("second setup: status = %d, want 409", second.StatusCode)
	}
	if !signsIn(t, base, testPassword) {
		t.Error("the second setup call overwrote the password")
	}

	me := getWithCookie(t, base, "/admin/me", cookie)
	me.Body.Close()
	if me.StatusCode != http.StatusOK {
		t.Errorf("/admin/me with the setup session: status = %d, want 200", me.StatusCode)
	}
}

func TestSetupRejectsAShortPassword(t *testing.T) {
	srv, _, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	resp := postJSON(t, base, http.MethodPost, "/admin/setup",
		map[string]string{"password": "short"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			t.Error("a rejected setup must not hand out a session")
		}
	}
}

func TestLoginAndLogout(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()
	setupAdmin(t, st)

	wrong := postJSON(t, base, http.MethodPost, "/admin/session",
		map[string]string{"password": "not-the-password"})
	wrong.Body.Close()
	if wrong.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: status = %d, want 401", wrong.StatusCode)
	}

	ok := postJSON(t, base, http.MethodPost, "/admin/session",
		map[string]string{"password": testPassword})
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("right password: status = %d, want 200", ok.StatusCode)
	}
	cookie := sessionCookieOf(t, ok)
	ok.Body.Close()
	if !cookie.HttpOnly {
		t.Error("the session cookie must be HttpOnly")
	}
	if strings.Contains(cookie.Value, testPassword) {
		t.Error("the session cookie must not embed the password")
	}

	authed := getWithCookie(t, base, "/admin/accounts", cookie)
	authed.Body.Close()
	if authed.StatusCode != http.StatusOK {
		t.Fatalf("authenticated request: status = %d, want 200", authed.StatusCode)
	}

	out := postJSON(t, base, http.MethodDelete, "/admin/session", nil, cookie)
	out.Body.Close()
	if out.StatusCode != http.StatusOK {
		t.Fatalf("logout: status = %d, want 200", out.StatusCode)
	}

	after := getWithCookie(t, base, "/admin/accounts", cookie)
	after.Body.Close()
	if after.StatusCode != http.StatusUnauthorized {
		t.Errorf("after logout: status = %d, want 401", after.StatusCode)
	}
}

// Signing in against a gateway nobody has claimed must say so rather than look
// like a wrong password, or the UI cannot route to the setup screen.
func TestLoginBeforeSetup(t *testing.T) {
	srv, _, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	resp := postJSON(t, base, http.MethodPost, "/admin/session",
		map[string]string{"password": testPassword})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

// Changing the password is the only revocation mechanism there is, so the
// sessions opened under the old one have to end with it.
func TestChangePasswordEndsOtherSessions(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()
	setupAdmin(t, st)

	first := postJSON(t, base, http.MethodPost, "/admin/session",
		map[string]string{"password": testPassword})
	other := sessionCookieOf(t, first)
	first.Body.Close()

	second := postJSON(t, base, http.MethodPost, "/admin/session",
		map[string]string{"password": testPassword})
	changer := sessionCookieOf(t, second)
	second.Body.Close()

	// The current password is required even though the session is live: a
	// browser someone walked away from should not lock its owner out.
	bad := postJSON(t, base, http.MethodPost, "/admin/password", map[string]string{
		"current_password": "not-the-password",
		"new_password":     "brand-new-password",
	}, changer)
	bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong current password: status = %d, want 401", bad.StatusCode)
	}

	resp := postJSON(t, base, http.MethodPost, "/admin/password", map[string]string{
		"current_password": testPassword,
		"new_password":     "brand-new-password",
	}, changer)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("change password: status = %d, want 200", resp.StatusCode)
	}
	fresh := sessionCookieOf(t, resp)
	resp.Body.Close()

	stale := getWithCookie(t, base, "/admin/accounts", other)
	stale.Body.Close()
	if stale.StatusCode != http.StatusUnauthorized {
		t.Errorf("the other session survived the password change: status = %d", stale.StatusCode)
	}

	// The caller keeps working, on the session the change handed back.
	live := getWithCookie(t, base, "/admin/accounts", fresh)
	live.Body.Close()
	if live.StatusCode != http.StatusOK {
		t.Errorf("the changing session was not carried over: status = %d", live.StatusCode)
	}

	if signsIn(t, base, testPassword) {
		t.Error("the old password still signs in")
	}
	if !signsIn(t, base, "brand-new-password") {
		t.Error("the new password does not sign in")
	}
}

func TestDeleteAccountReturnsToFirstRun(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()
	setupAdmin(t, st)

	in := postJSON(t, base, http.MethodPost, "/admin/session",
		map[string]string{"password": testPassword})
	cookie := sessionCookieOf(t, in)
	in.Body.Close()

	// Irreversible from the UI, so it asks for the password again rather than
	// treating the session as proof.
	bad := postJSON(t, base, http.MethodPost, "/admin/account/delete",
		map[string]string{"password": "not-the-password"}, cookie)
	bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: status = %d, want 401", bad.StatusCode)
	}

	gone := postJSON(t, base, http.MethodPost, "/admin/account/delete",
		map[string]string{"password": testPassword}, cookie)
	gone.Body.Close()
	if gone.StatusCode != http.StatusOK {
		t.Fatalf("delete: status = %d, want 200", gone.StatusCode)
	}

	after := getWithCookie(t, base, "/admin/accounts", cookie)
	after.Body.Close()
	if after.StatusCode != http.StatusUnauthorized {
		t.Errorf("the session outlived the account: status = %d", after.StatusCode)
	}

	status, err := http.Get(base + "/admin/setup")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		NeedsSetup bool `json:"needs_setup"`
	}
	if err := json.NewDecoder(status.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	status.Body.Close()
	if !body.NeedsSetup {
		t.Error("needs_setup = false after the account was deleted")
	}

	exists, err := st.AdminExists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("the account row survived the delete")
	}
}

// A fresh install with no password is not an open one.
func TestAdminAPIClosedBeforeSetup(t *testing.T) {
	srv, _, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	for _, path := range []string{"/admin/accounts", "/admin/me"} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s: status = %d, want 401", path, resp.StatusCode)
		}
	}
}
