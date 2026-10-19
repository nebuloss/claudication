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
		`DELETE FROM login_links WHERE expires_at <= ?`, ms(now)); err != nil {
		return "", time.Time{}, fmt.Errorf("prune expired links: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO login_links (token, expires_at) VALUES (?, ?)`, token, ms(expires),
	); err != nil {
		return "", time.Time{}, fmt.Errorf("store sign-in link: %w", err)
	}
	return token, expires, nil
}

// SpendLoginLink redeems a token: it works once, and never again.
//
// The row is deleted whether or not it was still valid, so a token cannot be
// retried and an expired one cannot be probed twice for a different answer.
// One statement rather than a read and a delete in a transaction: the
// redemption that deletes the row is the one that learns its expiry, and a
// second racing it finds nothing, with no transaction to hold or retry.
func (s *Store) SpendLoginLink(ctx context.Context, token string) error {
	var expiresAt int64
	err := s.db.QueryRowContext(ctx,
		`DELETE FROM login_links WHERE token = ? RETURNING expires_at`, token).Scan(&expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLinkInvalid
	}
	if err != nil {
		return fmt.Errorf("spend sign-in link: %w", err)
	}
	if !fromMS(expiresAt).After(time.Now()) {
		return ErrLinkInvalid
	}
	return nil
}
