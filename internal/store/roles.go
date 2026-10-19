package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Model aliases: see model_roles in schema.sql.
//
// Names the gateway's client configs ask for instead of real models, each
// bound to one real model. Three kinds:
//
//   - the two roles, claude-large and claude-small: the model doing the work
//     and the one for titles and summaries, the two slots every client has;
//   - one per model family the accounts serve — claude-opus, claude-haiku,
//     whatever families exist — found from the model list rather than
//     written down here, so a family Anthropic adds next year has an alias
//     the day an account lists it;
//   - custom ones an operator creates.
//
// A built-in alias left unbound follows the newest model of its family. A
// custom one is always bound: it has no family to follow. Every name is
// claude-* for the built-ins because two clients key on it — opencode applies
// its Claude handling only to an id containing "claude", and Claude Code's
// model picker lists only such names.

// The two role aliases.
const (
	RoleLarge = "claude-large"
	RoleSmall = "claude-small"
)

// Alias is one alias as the gateway knows it.
type Alias struct {
	Name string
	// Family is what a built-in alias follows while unbound; empty for a
	// custom one.
	Family  string
	Builtin bool
}

// IsRoleName reports whether a name is one of the two role aliases.
func IsRoleName(name string) bool {
	k := ModelKey(name)
	return k == RoleLarge || k == RoleSmall
}

// familyAlias is the shape of a family's alias: claude- and the family, with
// no version. A real model id always carries one.
var familyAlias = regexp.MustCompile(`^claude-([a-z]+)$`)

// AliasFamily is the family a built-in alias name follows: large → opus,
// small → haiku, claude-<family> → family; empty for anything else.
func AliasFamily(name string) string {
	switch k := ModelKey(name); {
	case k == RoleLarge:
		return "opus"
	case k == RoleSmall:
		return "haiku"
	default:
		if m := familyAlias.FindStringSubmatch(k); m != nil {
			return m[1]
		}
		return ""
	}
}

// aliasName is what a custom alias may be called: lower case, digits, dots and
// dashes, like a real id, so it sits in a config and a URL unquoted.
var aliasName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)

// clientName is what a client binding may name: the names api.ClientName
// gives — Claude Code, Codex, crush, opencode, or an unknown client's own
// product token — limited to the plain ones, since a binding is typed into
// the admin UI and printed back in a log line.
var clientName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$`)

// ModelRole is one binding: an alias, for the gateway (KeyID and Client
// empty), one key or one client, to a real model. An empty Model on a
// built-in alias's gateway binding follows its family.
type ModelRole struct {
	KeyID     string
	Client    string
	Role      string
	Model     string
	UpdatedAt time.Time
}

// ModelRoles lists every binding: the gateway's, every key's and every
// client's.
func (s *Store) ModelRoles(ctx context.Context) ([]ModelRole, error) {
	// NULL, the gateway's, sorts first, where '' did when that was its key_id.
	rows, err := s.db.QueryContext(ctx,
		`SELECT COALESCE(key_id, ''), COALESCE(client, ''), role, model, updated_at
		   FROM model_roles ORDER BY key_id, client, role`)
	if err != nil {
		return nil, fmt.Errorf("model roles: %w", err)
	}
	defer rows.Close()
	out := []ModelRole{}
	for rows.Next() {
		var (
			r      ModelRole
			update int64
		)
		if err := rows.Scan(&r.KeyID, &r.Client, &r.Role, &r.Model, &update); err != nil {
			return nil, fmt.Errorf("model role: %w", err)
		}
		r.UpdatedAt = fromMS(update)
		out = append(out, r)
	}
	return out, rows.Err()
}

// CustomAliases are the operator's own aliases: every gateway-wide binding
// whose name is not a built-in's shape.
func (s *Store) CustomAliases(ctx context.Context) ([]string, error) {
	all, err := s.ModelRoles(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range all {
		if r.KeyID == "" && r.Client == "" && AliasFamily(r.Role) == "" {
			out = append(out, r.Role)
		}
	}
	return out, nil
}

// The levels an alias can be bound at, as RoleBinding reports which one
// decided.
const (
	BoundByKey     = "key"
	BoundByClient  = "client"
	BoundByGateway = "gateway"
)

// RoleBinding is what an alias is bound to for one request: the key's own
// model if it has one, else the client's, else the gateway's, else "" —
// following its family. by is the level that decided, empty when nothing
// did. The key comes first because a key is what an operator sets apart on
// purpose — a script kept on a cheap model stays there whichever client
// drives it — and a client binding is the gateway's default for that client.
func (s *Store) RoleBinding(ctx context.Context, keyID, client, role string) (model, by string, err error) {
	all, err := s.ModelRoles(ctx)
	if err != nil {
		return "", "", err
	}
	role = ModelKey(role)
	var forClient, global string
	for _, r := range all {
		if r.Role != role || r.Model == "" {
			continue
		}
		switch {
		case keyID != "" && r.KeyID == keyID:
			return r.Model, BoundByKey, nil
		case client != "" && r.Client == client:
			forClient = r.Model
		case r.KeyID == "" && r.Client == "":
			global = r.Model
		}
	}
	switch {
	case forClient != "":
		return forClient, BoundByClient, nil
	case global != "":
		return global, BoundByGateway, nil
	}
	return "", "", nil
}

// ErrInvalidRole is a binding the operator asked for that cannot be made. Its
// text says why and is meant for them; anything else SetModelRole returns is
// the store failing, which is not theirs to read.
var ErrInvalidRole = errors.New("invalid alias binding")

type invalidRole string

func (e invalidRole) Error() string        { return string(e) }
func (e invalidRole) Is(target error) bool { return target == ErrInvalidRole }

// SetModelRole binds an alias. An empty model on a key removes the key's own
// binding, so the key follows the gateway; on the gateway it lets a built-in
// alias follow its family again. A custom alias is created by binding it on
// the gateway, and is always bound — DeleteAlias removes it.
func (s *Store) SetModelRole(ctx context.Context, keyID, role, model string) error {
	role, model, err := s.checkBinding(ctx, role, model)
	if err != nil {
		return err
	}
	if keyID != "" && model == "" {
		_, err := s.db.ExecContext(ctx,
			`DELETE FROM model_roles WHERE key_id = ? AND role = ?`, keyID, role)
		if err != nil {
			return fmt.Errorf("clear model role: %w", err)
		}
		return nil
	}
	if model == "" && AliasFamily(role) == "" {
		return invalidRole("a custom alias needs a model to point to")
	}
	// One upsert per partial index, since a conflict target names one. The
	// key's row is written only while the key exists, so a key deleted since
	// the caller looked is ErrKeyNotFound rather than a foreign key failure;
	// the foreign key stays as the guarantee.
	if keyID == "" {
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO model_roles (key_id, role, model, updated_at) VALUES (NULL, ?, ?, ?)
			 ON CONFLICT (role) WHERE key_id IS NULL AND client IS NULL DO UPDATE SET model = excluded.model,
			   updated_at = excluded.updated_at`,
			role, model, ms(time.Now()))
		if err != nil {
			return fmt.Errorf("set model role: %w", err)
		}
		return nil
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO model_roles (key_id, role, model, updated_at)
		 SELECT id, ?, ?, ? FROM api_keys WHERE id = ?
		 ON CONFLICT (key_id, role) WHERE key_id IS NOT NULL DO UPDATE SET model = excluded.model,
		   updated_at = excluded.updated_at`,
		role, model, ms(time.Now()), keyID)
	if err != nil {
		return fmt.Errorf("set model role: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrKeyNotFound
	}
	return nil
}

// SetClientRole binds an alias for every request one client makes, unless
// the request's key binds it itself. An empty model removes the binding, so
// the client follows the gateway again. client is as api.ClientName names it.
//
// Binding an alias here does not create it: a custom alias exists by its
// gateway binding, and a client row naming one that has none would serve
// nothing. The caller checks; DeleteAlias removes a custom alias's client rows
// with the rest.
func (s *Store) SetClientRole(ctx context.Context, client, role, model string) error {
	client = strings.TrimSpace(client)
	if !clientName.MatchString(client) {
		return invalidRole("a client is named as the request log names it: letters, digits, spaces, dots, dashes and underscores, up to 64")
	}
	role, model, err := s.checkBinding(ctx, role, model)
	if err != nil {
		return err
	}
	if model == "" {
		_, err := s.db.ExecContext(ctx,
			`DELETE FROM model_roles WHERE client = ? AND role = ?`, client, role)
		if err != nil {
			return fmt.Errorf("clear client model role: %w", err)
		}
		return nil
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO model_roles (client, role, model, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT (client, role) WHERE client IS NOT NULL DO UPDATE SET model = excluded.model,
		   updated_at = excluded.updated_at`,
		client, role, model, ms(time.Now()))
	if err != nil {
		return fmt.Errorf("set client model role: %w", err)
	}
	return nil
}

// checkBinding is what every level refuses alike: a name no alias could
// have, an alias pointing to itself, and one pointing to another alias. It
// returns the alias and the model as they are stored.
//
// Nothing here assumes the model is a Claude one: a target is whatever id an
// upstream answers to, so the only thing refused is an alias's own shape.
func (s *Store) checkBinding(ctx context.Context, role, model string) (string, string, error) {
	role = ModelKey(role)
	if !aliasName.MatchString(role) {
		return "", "", invalidRole("an alias is lower-case letters, digits, dots and dashes, up to 64")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return role, "", nil
	}
	if ModelKey(model) == role {
		return "", "", invalidRole("an alias cannot point to itself")
	}
	// The upstream knows only real ids, so an alias pointing to an alias
	// would send a name nothing answers.
	custom, err := s.CustomAliases(ctx)
	if err != nil {
		return "", "", err
	}
	if AliasFamily(model) != "" || slices.Contains(custom, ModelKey(model)) {
		return "", "", invalidRole("an alias points to a real model, not to another alias")
	}
	return role, model, nil
}

// ErrBuiltinAlias refuses deleting an alias the gateway makes itself.
var ErrBuiltinAlias = errors.New("a built-in alias cannot be deleted; bind it instead")

// DeleteAlias removes a custom alias from the gateway, every key and every
// client.
func (s *Store) DeleteAlias(ctx context.Context, name string) error {
	name = ModelKey(name)
	if AliasFamily(name) != "" {
		return ErrBuiltinAlias
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM model_roles WHERE role = ?`, name)
	if err != nil {
		return fmt.Errorf("delete alias: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrVirtualModelNotFound
	}
	return nil
}

// ErrVirtualModelNotFound is a delete of an alias that does not exist.
var ErrVirtualModelNotFound = errors.New("no such alias")

// SeenClients are the clients the request log has recorded, commonest first:
// what the admin UI offers to bind an alias for, beside the clients it knows.
func (s *Store) SeenClients(ctx context.Context) ([]string, error) {
	values, err := s.facet(ctx, "client", 50)
	if err != nil {
		return nil, err
	}
	// A request that named no client, or one whose name could not be bound,
	// is nothing to offer.
	out := make([]string, 0, len(values))
	for _, v := range values {
		if clientName.MatchString(v.Value) {
			out = append(out, v.Value)
		}
	}
	return out, nil
}
