package store

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/scrypt"
)

// The whole admin lifecycle in one test, because every scrypt derivation costs
// real time under -race: first-run state, claiming, refusing a second claim,
// verifying, changing with and without the right current password.
func TestAdminLifecycle(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if ok, err := st.AdminExists(ctx); err != nil || ok {
		t.Fatalf("AdminExists on a new store = %v, %v; want false", ok, err)
	}
	if _, err := st.VerifyAdmin(ctx, "whatever1"); !errors.Is(err, ErrNoAdmin) {
		t.Errorf("VerifyAdmin before setup = %v, want ErrNoAdmin", err)
	}
	if _, err := st.AdminUpdatedAt(ctx); !errors.Is(err, ErrNoAdmin) {
		t.Errorf("AdminUpdatedAt before setup = %v, want ErrNoAdmin", err)
	}
	// Length is counted in characters, not bytes: seven accented letters are
	// fourteen bytes and still too short.
	if err := st.CreateAdmin(ctx, "ééééééé"); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("CreateAdmin(short) = %v, want ErrPasswordTooShort", err)
	}

	if err := st.CreateAdmin(ctx, "correct horse"); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	if ok, _ := st.AdminExists(ctx); !ok {
		t.Error("AdminExists false after CreateAdmin")
	}
	// An exposed setup endpoint must not be a way to take the account over.
	if err := st.CreateAdmin(ctx, "attacker-pass"); !errors.Is(err, ErrAdminExists) {
		t.Errorf("second CreateAdmin = %v, want ErrAdminExists", err)
	}

	var stored string
	if err := st.DB().QueryRowContext(ctx, `SELECT password_hash FROM admin`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, fmt.Sprintf("scrypt$%d$", scryptN)) || strings.Contains(stored, "correct horse") {
		t.Errorf("stored hash %q is not an scrypt hash carrying its parameters", stored)
	}

	if ok, err := st.VerifyAdmin(ctx, "correct horse"); err != nil || !ok {
		t.Errorf("VerifyAdmin(right) = %v, %v", ok, err)
	}
	if ok, err := st.VerifyAdmin(ctx, "wrong horse"); err != nil || ok {
		t.Errorf("VerifyAdmin(wrong) = %v, %v", ok, err)
	}

	if err := st.ChangeAdminPassword(ctx, "wrong horse", "new password"); err == nil {
		t.Error("ChangeAdminPassword accepted a wrong current password")
	}
	if err := st.ChangeAdminPassword(ctx, "correct horse", "short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("ChangeAdminPassword(short next) = %v, want ErrPasswordTooShort", err)
	}
	if err := st.ChangeAdminPassword(ctx, "correct horse", "new password"); err != nil {
		t.Fatalf("ChangeAdminPassword: %v", err)
	}
	if ok, _ := st.VerifyAdmin(ctx, "new password"); !ok {
		t.Error("new password does not verify")
	}
	updated, err := st.AdminUpdatedAt(ctx)
	if err != nil || time.Since(updated) > time.Minute {
		t.Errorf("AdminUpdatedAt = %v, %v; want just now", updated, err)
	}
}

// Recovery from the CLI works with or without an existing account, and
// deleting the account must also kill the sign-in links minted for it.
func TestDeleteAdminAndRecover(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if _, _, err := st.MintLoginLink(ctx, 0); !errors.Is(err, ErrNoAdmin) {
		t.Errorf("MintLoginLink with no admin = %v, want ErrNoAdmin", err)
	}
	if err := st.SetAdminPassword(ctx, "short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("SetAdminPassword(short) = %v", err)
	}
	if err := st.SetAdminPassword(ctx, "recovered-pass"); err != nil {
		t.Fatalf("SetAdminPassword on an empty store: %v", err)
	}
	token, _, err := st.MintLoginLink(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}

	if err := st.DeleteAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.AdminExists(ctx); ok {
		t.Error("admin still exists after DeleteAdmin")
	}
	if err := st.SpendLoginLink(ctx, token); !errors.Is(err, ErrLinkInvalid) {
		t.Errorf("link minted for a deleted admin = %v, want ErrLinkInvalid", err)
	}
}

// A sign-in link is single-use, honours its lifetime, and expired ones are
// cleaned up so the table does not grow with every unused link.
func TestLoginLinks(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	// Insert the admin row directly: links never look at the hash, and an
	// scrypt derivation here would only slow the test.
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO admin (id, password_hash, created_at, updated_at) VALUES (1, 'x', '', '')`); err != nil {
		t.Fatal(err)
	}

	before := time.Now()
	token, expires, err := st.MintLoginLink(ctx, 0)
	if err != nil {
		t.Fatalf("MintLoginLink: %v", err)
	}
	if len(token) != 22 {
		t.Errorf("token %q has length %d, want 22", token, len(token))
	}
	if d := expires.Sub(before); d < LoginLinkTTL || d > LoginLinkTTL+time.Minute {
		t.Errorf("default lifetime %v, want %v", d, LoginLinkTTL)
	}
	if err := st.SpendLoginLink(ctx, token); err != nil {
		t.Fatalf("first spend: %v", err)
	}
	if err := st.SpendLoginLink(ctx, token); !errors.Is(err, ErrLinkInvalid) {
		t.Errorf("second spend = %v, want ErrLinkInvalid", err)
	}
	if err := st.SpendLoginLink(ctx, "never-minted"); !errors.Is(err, ErrLinkInvalid) {
		t.Errorf("unknown token = %v, want ErrLinkInvalid", err)
	}

	// An expired link is refused and also consumed, so it cannot be probed twice.
	stale, _, err := st.MintLoginLink(ctx, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := st.SpendLoginLink(ctx, stale); !errors.Is(err, ErrLinkInvalid) {
		t.Errorf("expired link = %v, want ErrLinkInvalid", err)
	}
	var n int
	st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM login_links WHERE token = ?`, stale).Scan(&n)
	if n != 0 {
		t.Error("expired link left behind after being presented")
	}

	// Minting prunes links that expired without being used.
	if _, _, err := st.MintLoginLink(ctx, time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, _, err := st.MintLoginLink(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM login_links`).Scan(&n)
	if n != 1 {
		t.Errorf("%d links stored, want only the live one", n)
	}
}

// The parameters travel with the hash so that raising the cost later does not
// lock out a password hashed under the old one; anything malformed fails
// closed.
func TestVerifyPasswordFormats(t *testing.T) {
	ctx := context.Background()
	salt := []byte("0123456789abcdef")
	derived, err := scrypt.Key([]byte("pw"), salt, 1024, scryptR, scryptP, scryptKeyLen)
	if err != nil {
		t.Fatal(err)
	}
	cheap := fmt.Sprintf("scrypt$1024$%s$%s", hex.EncodeToString(salt), hex.EncodeToString(derived))
	if !verifyPassword(ctx, "pw", cheap) {
		t.Error("a hash made with other parameters did not verify")
	}
	if verifyPassword(ctx, "other", cheap) {
		t.Error("wrong password verified")
	}

	saltHex, keyHex := hex.EncodeToString(salt), hex.EncodeToString(derived)
	for name, stored := range map[string]string{
		"empty":          "",
		"wrong scheme":   "bcrypt$1024$" + saltHex + "$" + keyHex,
		"too few parts":  "scrypt$1024$" + saltHex,
		"bad cost":       "scrypt$abc$" + saltHex + "$" + keyHex,
		"cost not pow 2": "scrypt$1000$" + saltHex + "$" + keyHex,
		"bad salt":       "scrypt$1024$zz$" + keyHex,
		"bad key":        "scrypt$1024$" + saltHex + "$zz",
	} {
		if verifyPassword(ctx, "pw", stored) {
			t.Errorf("%s: %q verified", name, stored)
		}
	}
}

// The KDF gate is what keeps unauthenticated requests from asking for 32 MiB
// each; a caller that gives up while queued must leave rather than wait.
func TestKDFGateHonoursCancellation(t *testing.T) {
	for i := 0; i < cap(kdfGate); i++ {
		kdfGate <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(kdfGate); i++ {
			<-kdfGate
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := hashPassword(ctx, "long enough"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("hashPassword with a full gate = %v, want DeadlineExceeded", err)
	}
	if verifyPassword(ctx, "pw", "scrypt$1024$00$00") {
		t.Error("verifyPassword succeeded without passing the gate")
	}
}
