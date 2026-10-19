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
		m, _, err := st.RoleBinding(ctx, keyID, "", role)
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
	if m, _, _ := st.RoleBinding(ctx, "k1", "", "team-fast"); m != "" {
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

// A request's alias is its key's binding, else its client's, else the
// gateway's, else its family's — and each answer says which level decided.
func TestClientRoleBindings(t *testing.T) {
	st := usageStore(t)
	ctx := context.Background()
	key, _, err := st.CreateKey(ctx, "laptop", KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	check := func(keyID, client, wantModel, wantBy string) {
		t.Helper()
		m, by, err := st.RoleBinding(ctx, keyID, client, RoleLarge)
		if err != nil {
			t.Fatal(err)
		}
		if m != wantModel || by != wantBy {
			t.Errorf("key %q client %q: %q by %q, want %q by %q", keyID, client, m, by, wantModel, wantBy)
		}
	}

	check(key.ID, "Codex", "", "")
	if err := st.SetModelRole(ctx, "", RoleLarge, "claude-opus-5"); err != nil {
		t.Fatal(err)
	}
	// Not a Claude id: the target is whatever an upstream answers to.
	if err := st.SetClientRole(ctx, "Codex", RoleLarge, "gpt-5"); err != nil {
		t.Fatal(err)
	}
	check("", "Codex", "gpt-5", BoundByClient)
	check(key.ID, "Codex", "gpt-5", BoundByClient)
	check(key.ID, "Claude Code", "claude-opus-5", BoundByGateway)
	check(key.ID, "", "claude-opus-5", BoundByGateway)

	// The key outranks the client.
	if err := st.SetModelRole(ctx, key.ID, RoleLarge, "claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	check(key.ID, "Codex", "claude-sonnet-5", BoundByKey)
	check("other", "Codex", "gpt-5", BoundByClient)

	// Rebinding replaces; clearing falls back to the gateway.
	if err := st.SetClientRole(ctx, "Codex", RoleLarge, "gpt-5-codex"); err != nil {
		t.Fatal(err)
	}
	check("other", "Codex", "gpt-5-codex", BoundByClient)
	if err := st.SetClientRole(ctx, "Codex", RoleLarge, ""); err != nil {
		t.Fatal(err)
	}
	check("other", "Codex", "claude-opus-5", BoundByGateway)

	// A client's binding is not the gateway's, and makes no custom alias.
	if err := st.SetClientRole(ctx, "crush", "team-fast", "claude-haiku-4-5"); err != nil {
		t.Fatal(err)
	}
	if custom, _ := st.CustomAliases(ctx); len(custom) != 0 {
		t.Errorf("a client binding made a custom alias: %v", custom)
	}
	all, _ := st.ModelRoles(ctx)
	var clients []string
	for _, r := range all {
		if r.Client != "" {
			clients = append(clients, r.Client+"/"+r.Role+"/"+r.Model)
		}
	}
	if !slices.Equal(clients, []string{"crush/team-fast/claude-haiku-4-5"}) {
		t.Errorf("client rows %v", clients)
	}

	for _, bad := range []struct{ client, role, model string }{
		{"", RoleLarge, "claude-opus-5"},
		{"evil\x1b[2J", RoleLarge, "claude-opus-5"},
		{"Codex", "Bad Name", "claude-opus-5"},
		{"Codex", RoleLarge, RoleLarge},
		{"Codex", RoleLarge, RoleSmall},
	} {
		if err := st.SetClientRole(ctx, bad.client, bad.role, bad.model); err == nil {
			t.Errorf("%q: %q → %q was accepted", bad.client, bad.role, bad.model)
		}
	}
}

// The schema itself holds a binding to one level, and one row per alias at
// each, whatever the Go code above it does.
func TestModelRoleLevelsAreEnforced(t *testing.T) {
	st := usageStore(t)
	ctx := context.Background()
	key, _, err := st.CreateKey(ctx, "laptop", KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	insert := func(keyID, client any) error {
		_, err := st.db.ExecContext(ctx,
			`INSERT INTO model_roles (key_id, client, role, model, updated_at)
			 VALUES (?, ?, 'claude-large', 'm', 0)`, keyID, client)
		return err
	}
	for _, row := range []struct{ keyID, client any }{{nil, nil}, {key.ID, nil}, {nil, "Codex"}} {
		if err := insert(row.keyID, row.client); err != nil {
			t.Fatalf("first %v/%v: %v", row.keyID, row.client, err)
		}
		if err := insert(row.keyID, row.client); err == nil {
			t.Errorf("a second %v/%v binding was accepted", row.keyID, row.client)
		}
	}
	if err := insert(nil, "crush"); err != nil {
		t.Errorf("another client's binding was refused: %v", err)
	}
	if err := insert(key.ID, "Codex"); err == nil {
		t.Errorf("a key-and-client binding was accepted")
	}
}

// The clients offered for binding are those the log recorded under a name a
// binding can hold, commonest first.
func TestSeenClients(t *testing.T) {
	st := usageStore(t)
	ctx := context.Background()
	for _, c := range []string{"Codex", "Codex", "Claude Code", "", "bad\tname"} {
		if _, err := st.db.ExecContext(ctx,
			`INSERT INTO usage_events (at, client, model, path, status)
			 VALUES (0, ?, 'm', '/v1/messages', 200)`, c); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.SeenClients(ctx)
	if err != nil || !slices.Equal(got, []string{"Codex", "Claude Code"}) {
		t.Errorf("SeenClients = %v, %v", got, err)
	}
}
