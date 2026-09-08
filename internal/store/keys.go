package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// KeyPrefix marks claudication-issued credentials so they are recognisable in logs
// and secret scanners.
const KeyPrefix = "clc_"

// prefixLen is how much of the random part we store in the clear to locate a
// row. It must be long enough to make collisions negligible but is not a
// secret: possession of the prefix alone proves nothing.
const prefixLen = 12

var ErrKeyNotFound = errors.New("api key not found")

type APIKey struct {
	ID         string
	Name       string
	Prefix     string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	KeyLimits
}

// KeyLimits is everything about a key an operator can change without reissuing
// it. Grouped because the three travel together through every layer, and a
// fourth positional argument to CreateKey was already one too many.
type KeyLimits struct {
	// RPMLimit is how many requests are allowed per RatePeriod. Named for the
	// column and for the common case; 0 means the gateway's own limit.
	RPMLimit int
	// RatePeriod is what that number is per. Zero reads as a minute, which is
	// what every key created before periods existed meant.
	RatePeriod time.Duration
	// TokenBudget is the tokens allowed per rolling day; 0 means unlimited.
	TokenBudget int64
}

// Period is RatePeriod with the default applied.
func (l KeyLimits) Period() time.Duration {
	if l.RatePeriod <= 0 {
		return time.Minute
	}
	return l.RatePeriod
}

func (l KeyLimits) validate() error {
	if l.RPMLimit < 0 {
		return errors.New("the rate limit cannot be negative")
	}
	if l.RatePeriod < 0 {
		return errors.New("the rate limit period cannot be negative")
	}
	if l.TokenBudget < 0 {
		return errors.New("the token budget cannot be negative")
	}
	return nil
}

// Display is the operator-facing short form, e.g. "clc_1a2b3c4d5e6f…".
func (k APIKey) Display() string { return KeyPrefix + k.Prefix + "…" }

func hashKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// CreateKey mints a credential and returns the plaintext exactly once. The
// caller must show it to the operator immediately; it is unrecoverable after.
func (s *Store) CreateKey(ctx context.Context, name string, limits KeyLimits) (APIKey, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return APIKey{}, "", errors.New("key name must not be empty")
	}
	if err := limits.validate(); err != nil {
		return APIKey{}, "", err
	}

	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return APIKey{}, "", fmt.Errorf("generate key: %w", err)
	}
	body := hex.EncodeToString(raw) // 48 hex chars
	plaintext := KeyPrefix + body

	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return APIKey{}, "", fmt.Errorf("generate key id: %w", err)
	}

	key := APIKey{
		ID:        hex.EncodeToString(idBytes),
		Name:      name,
		Prefix:    body[:prefixLen],
		CreatedAt: time.Now().UTC(),
		KeyLimits: limits,
	}

	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO api_keys (id, name, prefix, hash, created_at, rpm_limit, rate_period_s, token_budget)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		key.ID, key.Name, key.Prefix, hashKey(plaintext),
		key.CreatedAt.Format(time.RFC3339), key.RPMLimit,
		int64(key.Period().Seconds()), key.TokenBudget,
	); err != nil {
		return APIKey{}, "", fmt.Errorf("insert api key: %w", err)
	}
	return key, plaintext, nil
}

// Authenticate resolves a presented credential.
//
// The hash comparison is constant-time. auth2api's README claims "timing-safe
// API key validation" while actually doing a Set lookup on the plaintext; this
// is the real thing. The prefix lookup narrows to one row without revealing
// anything, and the decision itself never short-circuits on content.
func (s *Store) Authenticate(ctx context.Context, plaintext string) (APIKey, error) {
	body := strings.TrimPrefix(plaintext, KeyPrefix)
	if len(body) < prefixLen {
		return APIKey{}, ErrKeyNotFound
	}

	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, prefix, hash, created_at, last_used_at, rpm_limit, rate_period_s, token_budget
		   FROM api_keys WHERE prefix = ?`, body[:prefixLen])

	var (
		key                   APIKey
		storedHash, createdAt string
		lastUsed              sql.NullString
		periodSeconds         int64
	)
	err := row.Scan(&key.ID, &key.Name, &key.Prefix, &storedHash, &createdAt,
		&lastUsed, &key.RPMLimit, &periodSeconds, &key.TokenBudget)
	if errors.Is(err, sql.ErrNoRows) {
		// Spend a comparison anyway so a miss costs about the same as a hit.
		subtle.ConstantTimeCompare([]byte(hashKey(plaintext)), make([]byte, 64))
		return APIKey{}, ErrKeyNotFound
	}
	if err != nil {
		return APIKey{}, fmt.Errorf("look up api key: %w", err)
	}

	if subtle.ConstantTimeCompare([]byte(hashKey(plaintext)), []byte(storedHash)) != 1 {
		return APIKey{}, ErrKeyNotFound
	}

	key.RatePeriod = time.Duration(periodSeconds) * time.Second
	key.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	if lastUsed.Valid {
		if t, err := time.Parse(time.RFC3339, lastUsed.String); err == nil {
			key.LastUsedAt = &t
		}
	}
	return key, nil
}

// TouchKey records last use. Best-effort: a failure here must never fail the
// request it belongs to.
func (s *Store) TouchKey(ctx context.Context, id string) {
	_, _ = s.db.ExecContext(ctx,
		`UPDATE api_keys SET last_used_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), id)
}

func (s *Store) ListKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, prefix, created_at, last_used_at, rpm_limit, rate_period_s, token_budget
		   FROM api_keys ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()

	var keys []APIKey
	for rows.Next() {
		var (
			key           APIKey
			createdAt     string
			lastUsed      sql.NullString
			periodSeconds int64
		)
		if err := rows.Scan(&key.ID, &key.Name, &key.Prefix, &createdAt,
			&lastUsed, &key.RPMLimit, &periodSeconds, &key.TokenBudget); err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		key.RatePeriod = time.Duration(periodSeconds) * time.Second
		key.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		if lastUsed.Valid {
			if t, err := time.Parse(time.RFC3339, lastUsed.String); err == nil {
				key.LastUsedAt = &t
			}
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// DeleteKey removes a key. It is the only way to withdraw one.
//
// There is no separate revoke: revocation was one-way, so it was a delete that
// left a row behind. Usage rows carry their own copy of the key name, so the
// history a revoked row was supposedly preserving survives this anyway.
// UpdateKey changes a key's name and rate limit, leaving the secret alone.
//
// Without this the only way to correct a typo in a name, or to raise a limit
// set too low, was to delete the key and issue a new one — which means going
// round every client that holds it. The credential is what is expensive to
// change; its label is not.
func (s *Store) UpdateKey(ctx context.Context, id, name string, limits KeyLimits) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("a key needs a name")
	}
	if err := limits.validate(); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET name = ?, rpm_limit = ?, rate_period_s = ?, token_budget = ?
		  WHERE id = ?`,
		name, limits.RPMLimit, int64(limits.Period().Seconds()), limits.TokenBudget, id)
	if err != nil {
		return fmt.Errorf("update api key: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrKeyNotFound
	}
	return nil
}

func (s *Store) DeleteKey(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete api key: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrKeyNotFound
	}
	return nil
}
