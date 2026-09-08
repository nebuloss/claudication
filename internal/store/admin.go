package store

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"
)

// scrypt parameters. N=32768 is the interactive-login figure: roughly 100 ms
// and 32 MB per attempt on ordinary hardware, which is unnoticeable to the one
// operator signing in and ruinous to anyone guessing.
const (
	scryptN      = 32768
	scryptR      = 8
	scryptP      = 1
	scryptKeyLen = 32
)

// MinPasswordLength is deliberately modest. This is a single-operator gateway
// reached on a LAN or through a tunnel, and a rule strict enough to be annoying
// mostly produces passwords on sticky notes.
const MinPasswordLength = 8

var (
	ErrNoAdmin          = errors.New("no admin account has been set up yet")
	ErrAdminExists      = errors.New("an admin account already exists")
	ErrPasswordTooShort = fmt.Errorf("password must be at least %d characters", MinPasswordLength)
)

// kdfGate bounds how many scrypt derivations run at once.
//
// scrypt's cost is memory, and that is the whole point of it — but it also
// makes every endpoint that derives a password an amplifier: one small,
// unauthenticated request asks the process for 32 MiB. The per-IP rate
// limiter does not help here, because its bucket starts full, so a single
// caller can put a whole minute's budget in flight *simultaneously* — 60
// requests, ~1.9 GiB, on a box with 512 MB.
//
// Two at a time. Nobody signing in notices the queue; an attacker gets a
// queue instead of the machine's memory. This bounds concurrency, not rate:
// the limiters still do that.
var kdfGate = make(chan struct{}, 2)

func withKDF[T any](ctx context.Context, fn func() T) (T, error) {
	var zero T
	select {
	case kdfGate <- struct{}{}:
	case <-ctx.Done():
		return zero, ctx.Err()
	}
	defer func() { <-kdfGate }()
	return fn(), nil
}

// hashPassword renders scrypt$<N>$<salt-hex>$<derived-hex>.
//
// The parameters travel with the hash rather than being assumed, so raising the
// cost later does not lock out an account created under the old one.
func hashPassword(ctx context.Context, password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	type result struct {
		derived []byte
		err     error
	}
	got, err := withKDF(ctx, func() result {
		d, err := scrypt.Key([]byte(password), salt, scryptN, scryptR, scryptP, scryptKeyLen)
		return result{d, err}
	})
	if err != nil {
		return "", err
	}
	if got.err != nil {
		return "", fmt.Errorf("derive password hash: %w", got.err)
	}
	return fmt.Sprintf("scrypt$%d$%s$%s", scryptN,
		hex.EncodeToString(salt), hex.EncodeToString(got.derived)), nil
}

func verifyPassword(ctx context.Context, password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "scrypt" {
		return false
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	salt, err := hex.DecodeString(parts[2])
	if err != nil {
		return false
	}
	expected, err := hex.DecodeString(parts[3])
	if err != nil {
		return false
	}
	ok, err := withKDF(ctx, func() bool {
		derived, err := scrypt.Key([]byte(password), salt, n, scryptR, scryptP, len(expected))
		if err != nil {
			return false
		}
		return subtle.ConstantTimeCompare(derived, expected) == 1
	})
	return err == nil && ok
}

// AdminExists reports whether the gateway has been set up. A false answer is
// what puts the UI into its first-run state.
func (s *Store) AdminExists(ctx context.Context) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin`).Scan(&n); err != nil {
		return false, fmt.Errorf("check for an admin account: %w", err)
	}
	return n > 0, nil
}

// CreateAdmin sets the first password. It refuses to overwrite an existing
// account, so an exposed setup endpoint cannot be used to take one over.
func (s *Store) CreateAdmin(ctx context.Context, password string) error {
	if len([]rune(password)) < MinPasswordLength {
		return ErrPasswordTooShort
	}
	// Check before deriving, not after. The INSERT OR IGNORE below is still
	// what makes this safe against a race, but reaching it costs 32 MiB and
	// ~100 ms of scrypt, and on a gateway that is already claimed — which is
	// every gateway, for all of its life after the first minute — that work is
	// spent solely to produce a 409. An unauthenticated endpoint should not do
	// expensive work before it knows the work is wanted.
	switch exists, err := s.AdminExists(ctx); {
	case err != nil:
		return err
	case exists:
		return ErrAdminExists
	}
	hash, err := hashPassword(ctx, password)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)

	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO admin (id, password_hash, created_at, updated_at)
		 VALUES (1, ?, ?, ?)`, hash, now, now)
	if err != nil {
		return fmt.Errorf("create admin account: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAdminExists
	}
	return nil
}

// VerifyAdmin checks a password against the stored hash.
func (s *Store) VerifyAdmin(ctx context.Context, password string) (bool, error) {
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM admin WHERE id = 1`).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNoAdmin
	}
	if err != nil {
		return false, fmt.Errorf("load admin account: %w", err)
	}
	return verifyPassword(ctx, password, hash), nil
}

// ChangeAdminPassword requires the current one. Knowing a live session is not
// enough: a borrowed browser should not be able to lock the owner out.
func (s *Store) ChangeAdminPassword(ctx context.Context, current, next string) error {
	ok, err := s.VerifyAdmin(ctx, current)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("current password is incorrect")
	}
	return s.SetAdminPassword(ctx, next)
}

// SetAdminPassword replaces the password without checking the old one. The CLI
// uses it for recovery, where the operator has proved themselves by having
// shell access to the state directory.
func (s *Store) SetAdminPassword(ctx context.Context, password string) error {
	if len([]rune(password)) < MinPasswordLength {
		return ErrPasswordTooShort
	}
	hash, err := hashPassword(ctx, password)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)

	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO admin (id, password_hash, created_at, updated_at)
		 VALUES (1, ?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET password_hash = excluded.password_hash,
		                               updated_at    = excluded.updated_at`,
		hash, now, now); err != nil {
		return fmt.Errorf("set admin password: %w", err)
	}
	return nil
}

// DeleteAdmin removes the account, returning the gateway to its first-run
// state.
//
// Upstream accounts and API keys are left alone on purpose: this resets who can
// administer the gateway, not what it is connected to. Whoever sets the next
// password inherits both, which is the same position the first operator was in.
func (s *Store) DeleteAdmin(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM admin`); err != nil {
		return fmt.Errorf("delete admin account: %w", err)
	}
	// Outstanding sign-in links were passes to the account that just went away.
	_, _ = s.db.ExecContext(ctx, `DELETE FROM login_links`)
	return nil
}

// AdminUpdatedAt reports when the password last changed.
func (s *Store) AdminUpdatedAt(ctx context.Context) (time.Time, error) {
	var updated string
	err := s.db.QueryRowContext(ctx, `SELECT updated_at FROM admin WHERE id = 1`).Scan(&updated)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrNoAdmin
	}
	if err != nil {
		return time.Time{}, err
	}
	t, _ := time.Parse(time.RFC3339, updated)
	return t, nil
}
