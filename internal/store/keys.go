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
	// ModelsOff are the models this key may not ask for, as the operator
	// switched them; everything else is allowed. See api_key_models_off in
	// schema.sql.
	ModelsOff []string
	// SecretKept says the secret is stored sealed and can be shown again
	// (sealed_secret in schema.sql).
	SecretKept bool
}

// keyModelsOff is the column expression that reads a key's switches with it.
const keyModelsOff = `COALESCE((SELECT group_concat(model, char(31)) FROM api_key_models_off o
		  WHERE o.key_id = api_keys.id), '')`

// keyColumns is every column an APIKey is read back from, in the order
// scanKey takes them. One list for every query that returns whole keys, as
// usageEventColumns is for events: Key used to walk ListKeys, the titler kept
// its own copy of that walk, and Authenticate scanned its own shorter list and
// so never said whether the secret was kept.
const keyColumns = `id, name, prefix, created_at, last_used_at, rate_limit, rate_period_s, token_budget,
		` + keyModelsOff + `, sealed_secret IS NOT NULL`

// scanKey reads one row selected with keyColumns. lead are destinations for
// columns selected before them, as Authenticate's hash is.
func scanKey(sc scanner, lead ...any) (APIKey, error) {
	var (
		key           APIKey
		createdAt     int64
		lastUsed      sql.NullInt64
		periodSeconds int64
		modelsOff     string
	)
	dest := append(lead, &key.ID, &key.Name, &key.Prefix, &createdAt,
		&lastUsed, &key.RPMLimit, &periodSeconds, &key.TokenBudget, &modelsOff,
		&key.SecretKept)
	if err := sc.Scan(dest...); err != nil {
		return APIKey{}, err
	}
	key.ModelsOff = splitModels(modelsOff)
	key.RatePeriod = time.Duration(periodSeconds) * time.Second
	key.CreatedAt = fromMS(createdAt)
	key.LastUsedAt = optionalMS(lastUsed)
	return key, nil
}

func splitModels(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x1f")
}

// KeyLimits is everything about a key an operator can change without reissuing
// it. Grouped because the three travel together through every layer, and a
// fourth positional argument to CreateKey was already one too many.
type KeyLimits struct {
	// RPMLimit is how many requests are allowed per RatePeriod; 0 means the
	// gateway's own limit. Named for the common case and for the admin API's
	// rpm_limit, which predates the period; the column is rate_limit.
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

// ErrSecretNotKept is KeySecret's answer for a key whose secret was never
// stored: the gateway's own, or one ReissueUnkeptKeys has not reached yet.
var ErrSecretNotKept = errors.New("this key's secret was not kept; issue it a new one")

// KeySecret opens a key's sealed secret.
func (s *Store) KeySecret(ctx context.Context, id string) (string, error) {
	var sealed []byte
	err := s.db.QueryRowContext(ctx, `SELECT sealed_secret FROM api_keys WHERE id = ?`, id).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrKeyNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read api key secret: %w", err)
	}
	if len(sealed) == 0 {
		return "", ErrSecretNotKept
	}
	plaintext, err := s.sealer.Open(sealed)
	if err != nil {
		return "", fmt.Errorf("open api key secret: %w", err)
	}
	return plaintext, nil
}

// ReissueUnkeptKeys gives every key whose secret was never kept a new one,
// sealed, and returns the names of those it reissued. The keys in skip are left
// alone.
//
// A key from before secrets were kept had only its hash stored, so its Clients
// page could not show it or write a configuration with it; the operator had
// to regenerate it by hand, and the page carried a second shape for it. Run
// once at startup, this retires that case: every key the gateway holds can be
// shown. It breaks the clients holding those old secrets, deliberately — they
// fetch their configurations again from the key's Clients page — and only
// those: a key whose secret is kept is not touched.
//
// skip is for the gateway's own key, which nothing authenticates with and
// whose secret is thrown away by design (see the titles service).
func (s *Store) ReissueUnkeptKeys(ctx context.Context, skip map[string]bool) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name FROM api_keys WHERE sealed_secret IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("list unkept api keys: %w", err)
	}
	type unkept struct{ id, name string }
	var todo []unkept
	for rows.Next() {
		var u unkept
		if err := rows.Scan(&u.id, &u.name); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan unkept api key: %w", err)
		}
		if !skip[u.id] {
			todo = append(todo, u)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list unkept api keys: %w", err)
	}

	var names []string
	for _, u := range todo {
		body, plaintext, err := newSecret()
		if err != nil {
			return names, err
		}
		sealed, err := s.sealKey(plaintext)
		if err != nil {
			return names, err
		}
		if _, err := s.db.ExecContext(ctx,
			`UPDATE api_keys SET prefix = ?, hash = ?, sealed_secret = ? WHERE id = ? AND sealed_secret IS NULL`,
			body[:prefixLen], hashKey(plaintext), sealed, u.id); err != nil {
			return names, fmt.Errorf("reissue api key %s: %w", u.name, err)
		}
		names = append(names, u.name)
	}
	return names, nil
}

// sealKey seals a key's secret for sealed_secret.
func (s *Store) sealKey(plaintext string) ([]byte, error) {
	sealed, err := s.sealer.Seal(plaintext)
	if err != nil {
		return nil, fmt.Errorf("seal api key: %w", err)
	}
	return sealed, nil
}

// newSecret is a fresh credential: its random body, and the plaintext a client
// holds.
func newSecret() (body, plaintext string, err error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate key: %w", err)
	}
	body = hex.EncodeToString(raw) // 48 hex chars
	return body, KeyPrefix + body, nil
}

// RotateKey gives a key a new secret and returns its plaintext, as CreateKey
// does, kept sealed beside its hash in the same statement. Everything else
// about the key stays — its id, name, limits, model switches and the usage
// recorded against it — and the old secret stops authenticating as this
// returns.
//
// It exists for a secret that has gone somewhere it should not have: the way
// to withdraw it without deleting the key and losing its settings and history
// with it.
func (s *Store) RotateKey(ctx context.Context, id string) (APIKey, string, error) {
	body, plaintext, err := newSecret()
	if err != nil {
		return APIKey{}, "", err
	}
	sealed, err := s.sealKey(plaintext)
	if err != nil {
		return APIKey{}, "", err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET prefix = ?, hash = ?, sealed_secret = ? WHERE id = ?`,
		body[:prefixLen], hashKey(plaintext), sealed, id)
	if err != nil {
		return APIKey{}, "", fmt.Errorf("rotate api key: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return APIKey{}, "", ErrKeyNotFound
	}
	key, err := s.Key(ctx, id)
	if err != nil {
		return APIKey{}, "", err
	}
	return key, plaintext, nil
}

// CreateKey mints a credential and returns its plaintext. The secret is kept
// sealed beside its hash, in the same INSERT, so the key's Clients page can
// show it and its client configs again (see sealed_secret in schema.sql for
// why that is safe to keep).
//
// In the same statement because a key whose secret was not kept is one the
// startup reissue (ReissueUnkeptKeys) gives a new secret — which would break
// the client that was handed this one. Kept as a second call, a failure
// between the two left exactly that key behind.
func (s *Store) CreateKey(ctx context.Context, name string, limits KeyLimits) (APIKey, string, error) {
	return s.createKey(ctx, name, limits, true)
}

// CreateUnkeptKey mints a credential whose secret is not kept: only its hash
// is stored, and the plaintext returned here is the only copy there will ever
// be. It is for the gateway's own key, which nothing authenticates with and
// whose secret is thrown away by design (see the titles service).
func (s *Store) CreateUnkeptKey(ctx context.Context, name string, limits KeyLimits) (APIKey, string, error) {
	return s.createKey(ctx, name, limits, false)
}

func (s *Store) createKey(ctx context.Context, name string, limits KeyLimits, keep bool) (APIKey, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return APIKey{}, "", errors.New("key name must not be empty")
	}
	if err := limits.validate(); err != nil {
		return APIKey{}, "", err
	}

	body, plaintext, err := newSecret()
	if err != nil {
		return APIKey{}, "", err
	}
	var sealed []byte // NULL: not kept
	if keep {
		if sealed, err = s.sealKey(plaintext); err != nil {
			return APIKey{}, "", err
		}
	}

	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return APIKey{}, "", fmt.Errorf("generate key id: %w", err)
	}

	key := APIKey{
		ID:         hex.EncodeToString(idBytes),
		Name:       name,
		Prefix:     body[:prefixLen],
		CreatedAt:  time.Now().UTC(),
		KeyLimits:  limits,
		SecretKept: keep,
	}

	// The name with the key, so history never holds an id with no name.
	if err := s.inTx(ctx, "insert api key", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO api_keys (id, name, prefix, hash, created_at, rate_limit, rate_period_s, token_budget,
			                       sealed_secret)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			key.ID, key.Name, key.Prefix, hashKey(plaintext),
			ms(key.CreatedAt), key.RPMLimit,
			int64(key.Period().Seconds()), key.TokenBudget, sealed,
		); err != nil {
			return fmt.Errorf("insert api key: %w", err)
		}
		return rememberName(ctx, tx, nameKey, key.ID, key.Name)
	}); err != nil {
		return APIKey{}, "", err
	}
	key.CreatedAt = fromMS(ms(key.CreatedAt))
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
		`SELECT hash, `+keyColumns+` FROM api_keys WHERE prefix = ?`, body[:prefixLen])

	var storedHash string
	key, err := scanKey(row, &storedHash)
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
	return key, nil
}

// Key reads one key by id.
func (s *Store) Key(ctx context.Context, id string) (APIKey, error) {
	key, err := scanKey(s.db.QueryRowContext(ctx,
		`SELECT `+keyColumns+` FROM api_keys WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, ErrKeyNotFound
	}
	if err != nil {
		return APIKey{}, fmt.Errorf("load api key: %w", err)
	}
	return key, nil
}

// TouchKey records last use, at most once a minute per key (see lastUse).
// Best-effort: a failure here must never fail the request it belongs to.
func (s *Store) TouchKey(ctx context.Context, id string) {
	now := time.Now()
	if !s.keysUsed.due(id, now) {
		return
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at = ? WHERE id = ?`, ms(now), id)
}

func (s *Store) ListKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+keyColumns+` FROM api_keys ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()

	var keys []APIKey
	for rows.Next() {
		key, err := scanKey(rows)
		if err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

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
	return s.inTx(ctx, "update api key", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE api_keys SET name = ?, rate_limit = ?, rate_period_s = ?, token_budget = ?
			  WHERE id = ?`,
			name, limits.RPMLimit, int64(limits.Period().Seconds()), limits.TokenBudget, id)
		if err != nil {
			return fmt.Errorf("update api key: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrKeyNotFound
		}
		// A rename is what every past request of this key is shown under.
		return rememberName(ctx, tx, nameKey, id, name)
	})
}

// DeleteKey removes a key. It is the only way to withdraw one.
//
// There is no separate revoke: revocation was one-way, so it was a delete that
// left a row behind. The key's name outlives it in the names table, so its
// history still reads as this key.
//
// Its model switches and model-role bindings go with it by foreign key, in
// the same statement.
func (s *Store) DeleteKey(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete api key: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrKeyNotFound
	}
	s.keysUsed.forget(id)
	return nil
}
