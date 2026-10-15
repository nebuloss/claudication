package store

import (
	"context"
	"slices"
	"testing"
)

// An alias is the key's binding, else the gateway's, else (for a built-in)
// "" — following its family; a key's binding cleared follows the gateway
// again; and a key's bindings go with the key.
func TestModelRoleBindings(t *testing.T) {
	st := usageStore(t)
	ctx := context.Background()
	key, _, err := st.CreateKey(ctx, "laptop", KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	bound := func(keyID, role string) string {
		t.Helper()
		m, err := st.RoleBinding(ctx, keyID, role)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}

	if got := bound(key.ID, RoleLarge); got != "" {
		t.Errorf("nothing set: %q, want following the family", got)
	}
	if err := st.SetModelRole(ctx, "", RoleLarge, "claude-opus-5"); err != nil {
		t.Fatal(err)
	}
	if got := bound(key.ID, RoleLarge); got != "claude-opus-5" {
		t.Errorf("gateway's binding: %q", got)
	}
	if err := st.SetModelRole(ctx, key.ID, "Claude-Large", "claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	if got := bound(key.ID, RoleLarge); got != "claude-sonnet-5" {
		t.Errorf("key's binding: %q", got)
	}
	if got := bound("other", RoleLarge); got != "claude-opus-5" {
		t.Errorf("another key: %q", got)
	}
	if err := st.SetModelRole(ctx, key.ID, RoleLarge, ""); err != nil {
		t.Fatal(err)
	}
	if got := bound(key.ID, RoleLarge); got != "claude-opus-5" {
		t.Errorf("after clearing the key's: %q", got)
	}
	if err := st.SetModelRole(ctx, "", RoleLarge, ""); err != nil {
		t.Fatal(err)
	}
	if got := bound(key.ID, RoleLarge); got != "" {
		t.Errorf("gateway back to following the family: %q", got)
	}

	for _, bad := range []struct{ role, model string }{
		{"Bad Name", "claude-opus-5"},
		{RoleLarge, RoleLarge},
		{"team-model", ""}, // a custom alias always points somewhere
	} {
		if err := st.SetModelRole(ctx, "", bad.role, bad.model); err == nil {
			t.Errorf("%q → %q was accepted", bad.role, bad.model)
		}
	}

	_ = st.SetModelRole(ctx, key.ID, RoleSmall, "claude-haiku-4-5")
	if err := st.DeleteKey(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	all, _ := st.ModelRoles(ctx)
	for _, r := range all {
		if r.KeyID == key.ID {
			t.Errorf("binding left behind: %+v", r)
		}
	}
}

// A custom alias is any name not shaped like a built-in's, made by binding it
// on the gateway, listed, and deleted with every key's binding of it; a
// built-in cannot be deleted.
func TestCustomAliases(t *testing.T) {
	st := usageStore(t)
	ctx := context.Background()
	if err := st.SetModelRole(ctx, "", "team-fast", "claude-haiku-4-5"); err != nil {
		t.Fatal(err)
	}
	_ = st.SetModelRole(ctx, "", "claude-opus", "claude-opus-5")
	_ = st.SetModelRole(ctx, "k1", "team-fast", "claude-sonnet-5")
	custom, err := st.CustomAliases(ctx)
	if err != nil || !slices.Equal(custom, []string{"team-fast"}) {
		t.Fatalf("custom aliases = %v, %v", custom, err)
	}
	if err := st.DeleteAlias(ctx, "claude-opus"); err != ErrBuiltinAlias {
		t.Errorf("deleting a built-in: %v", err)
	}
	if err := st.DeleteAlias(ctx, "team-fast"); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.RoleBinding(ctx, "k1", "team-fast"); m != "" {
		t.Errorf("a key's binding outlived the alias: %q", m)
	}
	if err := st.DeleteAlias(ctx, "team-fast"); err != ErrVirtualModelNotFound {
		t.Errorf("deleting it twice: %v", err)
	}

	for name, want := range map[string]string{
		"claude-large": "opus", "claude-small": "haiku", "claude-mythos": "mythos",
		"claude-opus-5-5": "", "team-fast": "",
	} {
		if got := AliasFamily(name); got != want {
			t.Errorf("AliasFamily(%q) = %q, want %q", name, got, want)
		}
	}
}
