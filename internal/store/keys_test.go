package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestCreateAndAuthenticate(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	key, plaintext, err := st.CreateKey(ctx, "laptop", 0, 0)
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if !strings.HasPrefix(plaintext, KeyPrefix) {
		t.Errorf("plaintext %q lacks prefix %q", plaintext, KeyPrefix)
	}

	got, err := st.Authenticate(ctx, plaintext)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got.ID != key.ID {
		t.Errorf("ID = %q, want %q", got.ID, key.ID)
	}
	if got.Name != "laptop" {
		t.Errorf("Name = %q", got.Name)
	}
}

// The plaintext must not be recoverable from the database.
func TestPlaintextIsNotStored(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	_, plaintext, err := st.CreateKey(ctx, "laptop", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := st.DB().QueryRowContext(ctx, `SELECT hash FROM api_keys`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hash, strings.TrimPrefix(plaintext, KeyPrefix)) {
		t.Error("stored hash contains the plaintext key")
	}
}

func TestAuthenticateRejects(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	_, plaintext, err := st.CreateKey(ctx, "laptop", 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"empty":          "",
		"garbage":        "not-a-key",
		"right prefix":   plaintext[:len(KeyPrefix)+prefixLen] + strings.Repeat("0", 36),
		"truncated":      plaintext[:len(plaintext)-1],
		"prefix missing": strings.TrimPrefix(plaintext, KeyPrefix),
	}
	for name, cred := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := st.Authenticate(ctx, cred); !errors.Is(err, ErrKeyNotFound) {
				t.Errorf("Authenticate(%q) err = %v, want ErrKeyNotFound", cred, err)
			}
		})
	}
}

func TestRevoke(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	key, plaintext, err := st.CreateKey(ctx, "laptop", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeKey(ctx, key.ID); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}

	// Revoked keys still authenticate at the store layer; the HTTP layer is
	// what refuses them, so attribution for past traffic keeps resolving.
	got, err := st.Authenticate(ctx, plaintext)
	if err != nil {
		t.Fatalf("Authenticate after revoke: %v", err)
	}
	if !got.Revoked() {
		t.Error("key should report as revoked")
	}
	if err := st.RevokeKey(ctx, "nope"); !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("RevokeKey(unknown) = %v, want ErrKeyNotFound", err)
	}
}

func TestListKeys(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if keys, err := st.ListKeys(ctx); err != nil || len(keys) != 0 {
		t.Fatalf("empty store: keys=%d err=%v", len(keys), err)
	}
	for _, n := range []string{"a", "b"} {
		if _, _, err := st.CreateKey(ctx, n, 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := st.ListKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("len(keys) = %d, want 2", len(keys))
	}
}
