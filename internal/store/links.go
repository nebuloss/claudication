package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

// LoginLinkTTL is how long a freshly minted sign-in link stays usable.
const LoginLinkTTL = 15 * time.Minute

var ErrLinkInvalid = errors.New("sign-in link is unknown, expired, or already used")

// newLinkToken returns 16 random bytes as 22 URL-safe characters — short
// enough to paste comfortably, far too large to guess.
func newLinkToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate link token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// MintLoginLink issues a single-use pass to the admin account, dropping any
// links that have expired on the way through.
//
// It refuses when no account exists: a gateway in its first-run state is
// claimed by setting a password, not by minting a way past one that is not
// there.
func (s *Store) MintLoginLink(ctx context.Context, ttl time.Duration) (string, time.Time, error) {
	exists, err := s.AdminExists(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	if !exists {
		return "", time.Time{}, ErrNoAdmin
	}

	if ttl <= 0 {
		ttl = LoginLinkTTL
	}
	token, err := newLinkToken()
	if err != nil {
		return "", time.Time{}, err
	}

	now := time.Now().UTC()
	expires := now.Add(ttl)

	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM login_links WHERE expires_at <= ?`, now.Format(time.RFC3339Nano)); err != nil {
		return "", time.Time{}, fmt.Errorf("prune expired links: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO login_links (token, created_at, expires_at) VALUES (?, ?, ?)`,
		token, now.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano),
	); err != nil {
		return "", time.Time{}, fmt.Errorf("store sign-in link: %w", err)
	}
	return token, expires, nil
}

// SpendLoginLink redeems a token: it works once, and never again.
//
// The row is deleted whether or not it was still valid, so a token cannot be
// retried and an expired one cannot be probed twice for a different answer.
func (s *Store) SpendLoginLink(ctx context.Context, token string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("spend sign-in link: %w", err)
	}
	defer tx.Rollback()

	var expiresAt string
	err = tx.QueryRowContext(ctx,
		`SELECT expires_at FROM login_links WHERE token = ?`, token).Scan(&expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLinkInvalid
	}
	if err != nil {
		return fmt.Errorf("look up sign-in link: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM login_links WHERE token = ?`, token); err != nil {
		return fmt.Errorf("spend sign-in link: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("spend sign-in link: %w", err)
	}

	expiry, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || !expiry.After(time.Now()) {
		return ErrLinkInvalid
	}
	return nil
}
