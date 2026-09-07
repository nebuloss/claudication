package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nebuloss/claudication/internal/store"
)

// noRedirectClient stops at the first response so the redirect itself is
// observable rather than followed.
func noRedirectClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// testPassword is long enough to clear MinPasswordLength and is never a real
// one; every test that needs an account uses it.
const testPassword = "correct-horse"

// setupAdmin puts the gateway past its first-run state.
func setupAdmin(t *testing.T, st *store.Store) {
	t.Helper()
	if err := st.CreateAdmin(context.Background(), testPassword); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
}

// mintLink sets up the account, if it is not already, and returns a sign-in
// link for it.
func mintLink(t *testing.T, st *store.Store, ttl time.Duration) string {
	t.Helper()
	ctx := context.Background()
	if exists, err := st.AdminExists(ctx); err != nil {
		t.Fatalf("AdminExists: %v", err)
	} else if !exists {
		setupAdmin(t, st)
	}
	token, _, err := st.MintLoginLink(ctx, ttl)
	if err != nil {
		t.Fatalf("MintLoginLink: %v", err)
	}
	return token
}

func TestLinkSignsInAndStripsToken(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	token := mintLink(t, st, store.LoginLinkTTL)

	resp, err := noRedirectClient().Get(base + "/?token=" + token)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want / with no token", loc)
	}

	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			session = c
		}
	}
	if session == nil {
		t.Fatal("no session cookie was set")
	}
	if !session.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	if strings.Contains(session.Value, token) {
		t.Error("session cookie must not embed the link token")
	}

	req, _ := http.NewRequest(http.MethodGet, base+"/admin/accounts", nil)
	req.AddCookie(session)
	authed, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer authed.Body.Close()
	if authed.StatusCode != http.StatusOK {
		t.Errorf("cookie from the link did not authenticate: status %d", authed.StatusCode)
	}
}

// The point of the design: a link works once. A leaked URL is a spent URL.
func TestLinkIsSingleUse(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	token := mintLink(t, st, store.LoginLinkTTL)
	client := noRedirectClient()

	first, err := client.Get(base + "/?token=" + token)
	if err != nil {
		t.Fatal(err)
	}
	first.Body.Close()
	if first.StatusCode != http.StatusSeeOther {
		t.Fatalf("first use: status = %d, want 303", first.StatusCode)
	}

	second, err := client.Get(base + "/?token=" + token)
	if err != nil {
		t.Fatal(err)
	}
	second.Body.Close()
	if second.StatusCode != http.StatusUnauthorized {
		t.Errorf("second use: status = %d, want 401", second.StatusCode)
	}
}

func TestLinkExpires(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	// Inserted directly: MintLoginLink treats a non-positive TTL as "use the
	// default", so an expired link cannot be minted through the public API.
	ctx := context.Background()
	setupAdmin(t, st)
	const token = "expired-link-fixture"
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO login_links (token, created_at, expires_at) VALUES (?, ?, ?)`,
		token, past, past); err != nil {
		t.Fatal(err)
	}

	resp, err := noRedirectClient().Get(base + "/?token=" + token)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for an expired link", resp.StatusCode)
	}
}

// A gateway in its first-run state has no account to be let into, so there is
// nothing to mint a pass to. Minting one anyway would be a way past a password
// that has not been chosen yet.
func TestLinkNeedsAnAccount(t *testing.T) {
	_, st, _ := newTestServer(t)

	_, _, err := st.MintLoginLink(context.Background(), store.LoginLinkTTL)
	if !errors.Is(err, store.ErrNoAdmin) {
		t.Fatalf("MintLoginLink without an account = %v, want ErrNoAdmin", err)
	}
}

// Deleting the account must take its outstanding links with it: a link minted
// under the old password is a pass to an account that no longer exists.
func TestLinkDiesWithTheAccount(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	token := mintLink(t, st, store.LoginLinkTTL)
	if err := st.DeleteAdmin(context.Background()); err != nil {
		t.Fatal(err)
	}

	resp, err := noRedirectClient().Get(base + "/?token=" + token)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 once the account is gone", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			t.Error("a rejected link must not set a session cookie")
		}
	}
}

func TestLinkRejectsUnknownToken(t *testing.T) {
	srv, _, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	resp, err := noRedirectClient().Get(base + "/ui/?token=totally-made-up")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// GET /admin/session is the curl-friendly entry point for the same exchange.
func TestLinkViaAdminSession(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	token := mintLink(t, st, store.LoginLinkTTL)

	resp, err := noRedirectClient().Get(base + "/admin/session?token=" + token)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}

	bare, err := noRedirectClient().Get(base + "/admin/session")
	if err != nil {
		t.Fatal(err)
	}
	defer bare.Body.Close()
	if bare.StatusCode != http.StatusBadRequest {
		t.Errorf("bare GET status = %d, want 400", bare.StatusCode)
	}
}

// Without a token the UI must still be served normally.
func TestUIWithoutTokenIsUnaffected(t *testing.T) {
	srv, _, _ := newTestServer(t)
	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
}

// A short link is the whole point; guard the length so it stays pasteable.
func TestLinkTokenIsShort(t *testing.T) {
	_, st, _ := newTestServer(t)
	token := mintLink(t, st, store.LoginLinkTTL)
	if len(token) != 22 {
		t.Errorf("token length = %d, want 22 (16 random bytes, base64url)", len(token))
	}
	if strings.ContainsAny(token, "+/=") {
		t.Errorf("token %q must be URL-safe without padding", token)
	}
}
