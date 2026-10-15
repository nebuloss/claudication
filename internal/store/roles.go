package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Model roles: see migration 028.

// The two roles every client config the gateway writes uses. Named claude-*
// because two clients key on it: opencode applies its Claude handling only
// to an id containing "claude", and Claude Code's model picker lists only
// such names.
const (
	RoleLarge = "claude-large"
	RoleSmall = "claude-small"
)

// Roles is both, in the order the admin UI shows them.
var Roles = []string{RoleLarge, RoleSmall}

// IsRole reports whether a model name is one of the roles.
func IsRole(model string) bool {
	k := ModelKey(model)
	return k == RoleLarge || k == RoleSmall
}

// RoleFamily is the family a role follows when it is automatic: the newest
// model of it. Large work goes to Opus; titles and summaries, run many times
// a session, to Haiku.
func RoleFamily(role string) string {
	if ModelKey(role) == RoleSmall {
		return "haiku"
	}
	return "opus"
}

// ModelRole is one binding: a role, for the gateway (KeyID empty) or one
// key, to a real model. An empty Model on the gateway's binding is automatic.
type ModelRole struct {
	KeyID     string
	Role      string
	Model     string
	UpdatedAt time.Time
}

// ModelRoles lists every binding: the gateway's and every key's.
func (s *Store) ModelRoles(ctx context.Context) ([]ModelRole, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key_id, role, model, updated_at FROM model_roles ORDER BY key_id, role`)
	if err != nil {
		return nil, fmt.Errorf("model roles: %w", err)
	}
	defer rows.Close()
	out := []ModelRole{}
	for rows.Next() {
		var (
			r      ModelRole
			update string
		)
		if err := rows.Scan(&r.KeyID, &r.Role, &r.Model, &update); err != nil {
			return nil, fmt.Errorf("model role: %w", err)
		}
		r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, update)
		out = append(out, r)
	}
	return out, rows.Err()
}

// RoleBinding is what a role is bound to for one key: the key's own model if
// it has one, else the gateway's, else "" for automatic.
func (s *Store) RoleBinding(ctx context.Context, keyID, role string) (string, error) {
	all, err := s.ModelRoles(ctx)
	if err != nil {
		return "", err
	}
	role = ModelKey(role)
	global := ""
	for _, r := range all {
		if r.Role != role {
			continue
		}
		if keyID != "" && r.KeyID == keyID && r.Model != "" {
			return r.Model, nil
		}
		if r.KeyID == "" {
			global = r.Model
		}
	}
	return global, nil
}

// SetModelRole binds a role. For the gateway an empty model means automatic;
// for a key it removes the key's own binding, so the key follows the gateway.
func (s *Store) SetModelRole(ctx context.Context, keyID, role, model string) error {
	role = ModelKey(role)
	if !IsRole(role) {
		return fmt.Errorf("%q is not a model role; they are %s", role, strings.Join(Roles, " and "))
	}
	model = strings.TrimSpace(model)
	if IsRole(model) {
		return errors.New("a role is bound to a real model, not to another role")
	}
	if keyID != "" && model == "" {
		_, err := s.db.ExecContext(ctx,
			`DELETE FROM model_roles WHERE key_id = ? AND role = ?`, keyID, role)
		if err != nil {
			return fmt.Errorf("clear model role: %w", err)
		}
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO model_roles (key_id, role, model, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT (key_id, role) DO UPDATE SET model = excluded.model,
		   updated_at = excluded.updated_at`,
		keyID, role, model, stamp(time.Now()))
	if err != nil {
		return fmt.Errorf("set model role: %w", err)
	}
	return nil
}
