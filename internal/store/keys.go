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

	"claudication/internal/service/clients"
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
	// Disabled is a key whose client the operator switched off (clients in
	// schema.sql): the gateway refuses it before routing anything. Read with
	// the key, so the request path learns it from the lookup it already makes.
	Disabled bool
	// ClientType is the type of the client holding the key, "" for a key
	// that is no client (the gateway's own); ClientMode is how it names
	// models. A binding-mode client asks for its own slot names and nothing
	// else (internal/service/models, Route).
	ClientType string
	ClientMode clients.Mode
	// NameChats is the client's switch for the gateway naming its chats
	// (clients in schema.sql), read with the key for the request path. It
	// means nothing for a type that names its own chats, which has no switch
	// (clients.Type, NamesItsChats): the column is left as it was for those,
	// rather than dropped, because Codex and custom clients still need it.
	NameChats bool
	// FitImages is the client's switch for capping images in a many-image
	// request (fit_images in schema.sql), read with the key for the request
	// path as NameChats is.
	FitImages bool
}

// NamesSlots reports whether the key's client asks for its own slot names
// rather than real models: every typed client, and a custom one in binding
// mode. ModelsOff is a direct-mode client's, and does not apply here.
func (k APIKey) NamesSlots() bool {
	return k.ClientType != "" && k.ClientMode != clients.Direct
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
		` + keyModelsOff + `, sealed_secret IS NOT NULL,
		COALESCE((SELECT c.disabled_at IS NOT NULL FROM clients c WHERE c.key_id = api_keys.id), 0),
		COALESCE((SELECT c.type FROM clients c WHERE c.key_id = api_keys.id), ''),
		COALESCE((SELECT c.mode FROM clients c WHERE c.key_id = api_keys.id), ''),
		COALESCE((SELECT c.name_chats FROM clients c WHERE c.key_id = api_keys.id), 0),
		COALESCE((SELECT c.fit_images FROM clients c WHERE c.key_id = api_keys.id), 0)`

// scanKey reads one row selected with keyColumns. lead are destinations for
// columns selected before them, as Authenticate's hash is.
func scanKey(sc scanner, lead ...any) (APIKey, error) {
	return scanKeyAround(sc, lead, nil)
}

// scanKeyAround is scanKey with destinations for columns selected after
// keyColumns too, as a client's are.
func scanKeyAround(sc scanner, lead, trail []any) (APIKey, error) {
	var (
		key           APIKey
		createdAt     int64
		lastUsed      sql.NullInt64
		periodSeconds int64
		modelsOff     string
		mode          string
	)
	dest := append(append([]any{}, lead...), &key.ID, &key.Name, &key.Prefix, &createdAt,
		&lastUsed, &key.RPMLimit, &periodSeconds, &key.TokenBudget, &modelsOff,
		&key.SecretKept, &key.Disabled, &key.ClientType, &mode, &key.NameChats, &key.FitImages)
	dest = append(dest, trail...)
	if err := sc.Scan(dest...); err != nil {
		return APIKey{}, err
	}
	key.ModelsOff = splitModels(modelsOff)
	key.ClientMode = clients.Mode(mode)

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
// stored. Every key made since 026 keeps it, and the one that did not — the
// gateway's own titling key — went at 032, so this is a database damaged by
// hand.
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
	// A new secret is a client to set up again: its page offers the key and
	// the install command until the new one is used.
	if _, err := s.db.ExecContext(ctx, `UPDATE clients SET configured_at = NULL WHERE key_id = ?`, id); err != nil {
		return APIKey{}, "", fmt.Errorf("rotate api key: %w", err)
	}
	s.keysUsed.forget(id)
	key, err := s.Key(ctx, id)
	if err != nil {
		return APIKey{}, "", err
	}
	return key, plaintext, nil
}

// CreateKey mints a credential and returns its plaintext. The secret is kept
// sealed beside its hash, in the same INSERT, so the client's page can show it
// and its configs again until it is first used (see sealed_secret in
// schema.sql for why that is safe to keep). A key with no client is made only
// by tests; every key the gateway issues is a client's (CreateClient).
func (s *Store) CreateKey(ctx context.Context, name string, limits KeyLimits) (APIKey, string, error) {
	var (
		key       APIKey
		plaintext string
	)
	if err := s.inTx(ctx, "insert api key", func(tx *sql.Tx) error {
		var err error
		key, plaintext, err = s.insertKey(ctx, tx, name, limits)
		return err
	}); err != nil {
		return APIKey{}, "", err
	}
	return key, plaintext, nil
}

// insertKey writes a new key inside tx, with its name, so history never holds
// an id with no name: the half of CreateKey that creating a client shares.
func (s *Store) insertKey(ctx context.Context, tx *sql.Tx, name string, limits KeyLimits) (APIKey, string, error) {
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
	sealed, err := s.sealKey(plaintext)
	if err != nil {
		return APIKey{}, "", err
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
		SecretKept: true,
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO api_keys (id, name, prefix, hash, created_at, rate_limit, rate_period_s, token_budget,
		                       sealed_secret)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		key.ID, key.Name, key.Prefix, hashKey(plaintext),
		ms(key.CreatedAt), key.RPMLimit,
		int64(key.Period().Seconds()), key.TokenBudget, sealed,
	); err != nil {
		return APIKey{}, "", fmt.Errorf("insert api key: %w", err)
	}
	if err := rememberName(ctx, tx, nameKey, key.ID, key.Name); err != nil {
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
	if _, err := s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at = ? WHERE id = ?`, ms(now), id); err != nil {
		// Not written, so not a write to wait a minute after.
		s.keysUsed.forget(id)
		return
	}
	// A key used is a client set up with it: the first use is when its page
	// stops offering the key and the install command. In the same minute's
	// write budget, so the request path pays nothing more for it.
	_ = s.MarkConfigured(ctx, id)
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
