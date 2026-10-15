package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Virtual models: see migration 027.

// VirtualModel is a name clients ask for and the real models behind it, in
// one scope: gateway-wide (KeyID and Client empty), one key (Client empty),
// or one client app on one key.
type VirtualModel struct {
	KeyID       string
	Client      string
	Name        string
	DisplayName string
	// Targets are real model ids, most preferred first.
	Targets   []string
	UpdatedAt time.Time
}

// ErrVirtualModelNotFound is a lookup or delete of a name that is not one.
var ErrVirtualModelNotFound = errors.New("no such virtual model")

// virtualName is what a virtual model may be called: lower case, digits,
// dots and dashes, as the real ids are, so it sits in a client's config and a
// URL without quoting.
var virtualName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)

// Validate checks a virtual model before it is stored.
func (v VirtualModel) Validate() error {
	if !virtualName.MatchString(v.Name) {
		return errors.New("a virtual model's name is lower-case letters, digits, dots and dashes, up to 64")
	}
	if v.Client != "" && v.KeyID == "" {
		return errors.New("a mapping for one client app belongs to a key")
	}
	if len(v.Targets) == 0 {
		return errors.New("a virtual model needs at least one real model to point to")
	}
	seen := map[string]bool{}
	for _, t := range v.Targets {
		if strings.TrimSpace(t) == "" {
			return errors.New("a target cannot be empty")
		}
		if ModelKey(t) == ModelKey(v.Name) {
			return errors.New("a virtual model cannot point to itself")
		}
		if seen[ModelKey(t)] {
			return fmt.Errorf("%s is listed twice", t)
		}
		seen[ModelKey(t)] = true
	}
	return nil
}

// VirtualModels lists every virtual model in every scope: gateway-wide first,
// then by key and client, each by name.
func (s *Store) VirtualModels(ctx context.Context) ([]VirtualModel, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key_id, client, name, display_name, targets, updated_at FROM virtual_models
		  ORDER BY key_id, client, name`)
	if err != nil {
		return nil, fmt.Errorf("virtual models: %w", err)
	}
	defer rows.Close()
	out := []VirtualModel{}
	for rows.Next() {
		var (
			v               VirtualModel
			targets, update string
		)
		if err := rows.Scan(&v.KeyID, &v.Client, &v.Name, &v.DisplayName, &targets, &update); err != nil {
			return nil, fmt.Errorf("virtual model: %w", err)
		}
		v.Targets = splitModels(targets)
		v.UpdatedAt, _ = time.Parse(time.RFC3339Nano, update)
		out = append(out, v)
	}
	return out, rows.Err()
}

// scopeRank is how specific a mapping is for one key and client: lower wins,
// -1 means it does not apply.
func (v VirtualModel) scopeRank(keyID, client string) int {
	switch {
	case v.KeyID == "" && v.Client == "":
		return 2
	case v.KeyID == keyID && v.Client == "":
		return 1
	case v.KeyID == keyID && v.Client == client:
		return 0
	}
	return -1
}

// EffectiveVirtualModels is every virtual name a key's client app can ask for,
// each mapped by the most specific scope that has it: that key and app, then
// that key, then the gateway. What /v1/models offers it and its config lists.
func (s *Store) EffectiveVirtualModels(ctx context.Context, keyID, client string) ([]VirtualModel, error) {
	all, err := s.VirtualModels(ctx)
	if err != nil {
		return nil, err
	}
	best := map[string]VirtualModel{}
	rank := map[string]int{}
	var order []string
	for _, v := range all {
		r := v.scopeRank(keyID, client)
		if r < 0 {
			continue
		}
		k := ModelKey(v.Name)
		if prev, ok := rank[k]; ok && prev <= r {
			continue
		}
		if _, ok := rank[k]; !ok {
			order = append(order, k)
		}
		best[k], rank[k] = v, r
	}
	out := make([]VirtualModel, 0, len(order))
	for _, k := range order {
		out = append(out, best[k])
	}
	return out, nil
}

// ResolveVirtualModel is the mapping a request's model name has for its key
// and client app, by ModelKey so case and a trailing date do not matter.
func (s *Store) ResolveVirtualModel(ctx context.Context, keyID, client, name string) (VirtualModel, error) {
	effective, err := s.EffectiveVirtualModels(ctx, keyID, client)
	if err != nil {
		return VirtualModel{}, err
	}
	key := ModelKey(name)
	for _, v := range effective {
		if ModelKey(v.Name) == key {
			return v, nil
		}
	}
	return VirtualModel{}, ErrVirtualModelNotFound
}

// PutVirtualModel creates or replaces a virtual model in its scope.
func (s *Store) PutVirtualModel(ctx context.Context, v VirtualModel) error {
	v.Name = strings.TrimSpace(v.Name)
	for i, t := range v.Targets {
		v.Targets[i] = strings.TrimSpace(t)
	}
	if err := v.Validate(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO virtual_models (key_id, client, name, display_name, targets, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (key_id, client, name) DO UPDATE SET display_name = excluded.display_name,
		   targets = excluded.targets, updated_at = excluded.updated_at`,
		v.KeyID, v.Client, v.Name, strings.TrimSpace(v.DisplayName),
		strings.Join(v.Targets, "\x1f"), stamp(time.Now()))
	if err != nil {
		return fmt.Errorf("put virtual model: %w", err)
	}
	return nil
}

// DeleteVirtualModel removes one mapping from one scope. A narrower scope's
// removal lets the wider one through again; the gateway-wide one's removal
// leaves clients asking for a name nobody serves, and the upstream says so.
func (s *Store) DeleteVirtualModel(ctx context.Context, keyID, client, name string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM virtual_models WHERE key_id = ? AND client = ? AND name = ?`, keyID, client, name)
	if err != nil {
		return fmt.Errorf("delete virtual model: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrVirtualModelNotFound
	}
	return nil
}
