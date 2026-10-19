package store

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// A key's secret is kept sealed as it is made, and opens to exactly what was
// issued; rotating keeps the new one in its place; a key made without keeping
// it says so rather than failing obscurely.
func TestKeySecretIsKeptSealed(t *testing.T) {
	st := usageStore(t)
	ctx := context.Background()
	key, plaintext, err := st.CreateKey(ctx, "laptop", KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if !key.SecretKept {
		t.Error("CreateKey did not report the secret kept")
	}
	got, err := st.KeySecret(ctx, key.ID)
	if err != nil || got != plaintext {
		t.Fatalf("KeySecret = %q, %v", got, err)
	}
	if k, _ := st.Key(ctx, key.ID); !k.SecretKept {
		t.Error("SecretKept not reported")
	}
	// Sealed, not stored as text.
	var raw []byte
	_ = st.db.QueryRow(`SELECT sealed_secret FROM api_keys WHERE id = ?`, key.ID).Scan(&raw)
	if strings.Contains(string(raw), plaintext) {
		t.Error("the secret is readable in the database")
	}

	rotated, fresh, err := st.RotateKey(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.KeySecret(ctx, key.ID); err != nil || got != fresh || !rotated.SecretKept {
		t.Errorf("after rotating: %q, %v, kept %v; want the new secret kept", got, err, rotated.SecretKept)
	}

	unkept, _, err := st.CreateUnkeptKey(ctx, "internal", KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.KeySecret(ctx, unkept.ID); err != ErrSecretNotKept || unkept.SecretKept {
		t.Errorf("an unkept key: %v, kept %v; want ErrSecretNotKept", err, unkept.SecretKept)
	}
	if _, err := st.KeySecret(ctx, "nope"); err != ErrKeyNotFound {
		t.Errorf("unknown key: %v", err)
	}
}

// A new secret replaces the old at once and keeps everything else about the
// key — id, name, limits, model switches — which is the point of rotating
// rather than deleting and minting another.
func TestRotateKeyKeepsTheKeyAndChangesTheSecret(t *testing.T) {
	st := usageStore(t)
	ctx := context.Background()
	key, old, err := st.CreateKey(ctx, "laptop", KeyLimits{RPMLimit: 7, TokenBudget: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKeyModel(ctx, key.ID, "claude-opus-5", false); err != nil {
		t.Fatal(err)
	}

	rotated, fresh, err := st.RotateKey(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh == old || rotated.Prefix == key.Prefix {
		t.Fatal("the secret did not change")
	}
	if _, err := st.Authenticate(ctx, old); err != ErrKeyNotFound {
		t.Errorf("old secret: %v, want ErrKeyNotFound", err)
	}
	got, err := st.Authenticate(ctx, fresh)
	if err != nil {
		t.Fatalf("new secret: %v", err)
	}
	if got.ID != key.ID || got.Name != "laptop" || got.RPMLimit != 7 || got.TokenBudget != 1000 ||
		!slices.Equal(got.ModelsOff, []string{"claude-opus-5"}) {
		t.Errorf("rotated key lost something: %+v", got)
	}

	if _, _, err := st.RotateKey(ctx, "nope"); err != ErrKeyNotFound {
		t.Errorf("unknown key: %v", err)
	}
}

// A key's model switches: off under one spelling covers the other, they
// travel with the key into every request (Authenticate) and the keys screen
// (ListKeys), and they go with the key.
func TestKeyModelSwitches(t *testing.T) {
	st := usageStore(t)
	ctx := context.Background()
	key, plaintext, err := st.CreateKey(ctx, "script", KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}

	if err := st.SetKeyModel(ctx, key.ID, "claude-haiku-4-5-20251001", false); err != nil {
		t.Fatal(err)
	}
	// Off again under the alias: already off, not a second row.
	if err := st.SetKeyModel(ctx, key.ID, "claude-haiku-4-5", false); err != nil {
		t.Fatal(err)
	}

	authed, err := st.Authenticate(ctx, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(authed.ModelsOff, []string{"claude-haiku-4-5-20251001"}) {
		t.Errorf("Authenticate ModelsOff = %v", authed.ModelsOff)
	}
	if authed.Allows("claude-haiku-4-5") || !authed.Allows("claude-opus-5") || !authed.Allows("") {
		t.Error("Allows disagrees with the switch")
	}
	listed, err := st.Key(ctx, key.ID)
	if err != nil || len(listed.ModelsOff) != 1 {
		t.Errorf("Key = %+v, %v", listed, err)
	}

	// On by the alias clears the dated row.
	if err := st.SetKeyModel(ctx, key.ID, "claude-haiku-4-5", true); err != nil {
		t.Fatal(err)
	}
	if again, _ := st.Key(ctx, key.ID); len(again.ModelsOff) != 0 {
		t.Errorf("after on: %v", again.ModelsOff)
	}

	if err := st.SetKeyModel(ctx, "nope", "m", false); err != ErrKeyNotFound {
		t.Errorf("unknown key: %v", err)
	}

	// Deleting the key takes its switches with it.
	_ = st.SetKeyModel(ctx, key.ID, "claude-opus-5", false)
	if err := st.DeleteKey(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM api_key_models_off`).Scan(&n); err != nil || n != 0 {
		t.Errorf("switches left behind: %d, %v", n, err)
	}
}

// On upgrade, a key whose secret was never kept is reissued with one that is:
// its old secret stops authenticating, the new one is kept and works, the
// skipped key is left alone, and a key already kept is not touched.
func TestReissueUnkeptKeys(t *testing.T) {
	st := usageStore(t)
	ctx := context.Background()

	legacy, legacySecret, err := st.CreateUnkeptKey(ctx, "legacy", KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	own, _, err := st.CreateUnkeptKey(ctx, "gateway (internal)", KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	kept, keptSecret, err := st.CreateKey(ctx, "kept", KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}

	names, err := st.ReissueUnkeptKeys(ctx, map[string]bool{own.ID: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names, []string{"legacy"}) {
		t.Fatalf("reissued %v, want only the legacy key", names)
	}

	if _, err := st.Authenticate(ctx, legacySecret); err == nil {
		t.Error("the legacy key's old secret still authenticates")
	}
	fresh, err := st.KeySecret(ctx, legacy.ID)
	if err != nil {
		t.Fatalf("the reissued secret is not kept: %v", err)
	}
	if k, err := st.Authenticate(ctx, fresh); err != nil || k.ID != legacy.ID {
		t.Errorf("the reissued secret does not authenticate as its key: %v", err)
	}
	if _, err := st.KeySecret(ctx, own.ID); err != ErrSecretNotKept {
		t.Errorf("the skipped key was given a secret: %v", err)
	}
	if k, err := st.Authenticate(ctx, keptSecret); err != nil || k.ID != kept.ID {
		t.Errorf("a key already kept stopped authenticating: %v", err)
	}

	// Run again, nothing is left to do.
	if again, err := st.ReissueUnkeptKeys(ctx, map[string]bool{own.ID: true}); err != nil || len(again) != 0 {
		t.Errorf("second run reissued %v (%v)", again, err)
	}
}
