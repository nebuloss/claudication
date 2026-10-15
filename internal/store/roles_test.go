package store

import (
	"context"
	"testing"
)

// A role is the key's binding, else the gateway's, else automatic (""); a
// key's binding cleared follows the gateway again; and a key's bindings go
// with the key.
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
		t.Errorf("nothing set: %q, want automatic", got)
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
	if got := bound(key.ID, RoleSmall); got != "" {
		t.Errorf("the other role: %q", got)
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
		t.Errorf("gateway back to automatic: %q", got)
	}

	if err := st.SetModelRole(ctx, "", "claude-medium", "m"); err == nil {
		t.Error("a role that does not exist was accepted")
	}
	if err := st.SetModelRole(ctx, "", RoleLarge, RoleSmall); err == nil {
		t.Error("a role bound to another role was accepted")
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
