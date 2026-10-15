package store

import (
	"context"
	"slices"
	"testing"
)

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
