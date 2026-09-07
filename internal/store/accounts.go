package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nebuloss/claudication/internal/secret"
)

var ErrAccountNotFound = errors.New("account not found")

// Account is one logged-in upstream credential.
type Account struct {
	ID          string
	Provider    string
	Email       string
	AccountUUID string
	ExpiresAt   time.Time
	// RefreshExpiresAt is when re-authorisation becomes unavoidable. Zero when
	// the provider did not say.
	RefreshExpiresAt time.Time
	CreatedAt        time.Time
	LastRefreshAt    *time.Time
	LastUsedAt       *time.Time
	LastError        string
	DisabledAt       *time.Time
	// Position is the operator's priority order, lowest first. The pool serves
	// from the top of this list, not from whichever account looks quietest.
	Position int
	// Quota is what the upstream last reported about this account's
	// subscription windows. Zero-valued until a response has been seen.
	Quota AccountQuota
}

// AccountQuota is what /api/oauth/usage reported for this account — the same
// figures `/usage` prints in the client.
//
// The 5-hour window is what bites during a working session; the 7-day one is
// what runs out over a heavy week. Detail holds the server's own normalised
// `limits` array verbatim, including the per-model weekly rows the headline
// windows do not cover.
type AccountQuota struct {
	UpdatedAt time.Time
	// Percentages in [0,100], or -1 when never fetched. Unknown is
	// deliberately not zero: an account we have not asked about is unknown,
	// not idle, and the two should not route the same way.
	FiveHourUtil   float64
	FiveHourReset  time.Time
	FiveHourStatus string
	SevenDayUtil   float64
	SevenDayReset  time.Time
	SevenDayStatus string
	// Detail is the raw `limits` JSON, passed through to the UI untouched.
	Detail string
}

// Known reports whether the upstream has ever told us about this account.
func (q AccountQuota) Known() bool { return q.FiveHourUtil >= 0 || q.SevenDayUtil >= 0 }

// AtLimit is the percentage at which an account counts as out of room.
const AtLimit = 99.0

// Utilization is the binding window: whichever is fuller runs out first.
// Returns -1 when nothing is known.
func (q AccountQuota) Utilization() float64 {
	if q.SevenDayUtil > q.FiveHourUtil {
		return q.SevenDayUtil
	}
	return q.FiveHourUtil
}

// Allowed reports whether the upstream currently says the account may serve.
// An empty status means it never said, which is not a refusal.
func (q AccountQuota) Allowed() bool {
	ok := func(s string) bool { return s == "" || s == "allowed" }
	return ok(q.FiveHourStatus) && ok(q.SevenDayStatus)
}

func (a Account) Disabled() bool { return a.DisabledAt != nil }

// RefreshWindow reports how long until the refresh token dies, and whether we
// know at all. Once it passes, refreshing cannot recover the account.
func (a Account) RefreshWindow() (time.Duration, bool) {
	if a.RefreshExpiresAt.IsZero() {
		return 0, false
	}
	return time.Until(a.RefreshExpiresAt), true
}

// NeedsReauthSoon mirrors the client's own banner: warn inside three days.
func (a Account) NeedsReauthSoon() bool {
	d, ok := a.RefreshWindow()
	return ok && d <= 3*24*time.Hour
}

// Expired reports whether the access token is past its expiry.
func (a Account) Expired() bool { return time.Now().After(a.ExpiresAt) }

// Tokens carries the sensitive half, kept out of Account so that the common
// listing path cannot accidentally serialise a credential.
type Tokens struct {
	AccessToken  string
	RefreshToken string
}

// UpsertAccount stores a freshly authorised account, or replaces the tokens of
// one that already exists for this provider and email.
//
// Identity (email, uuid) is set at login and never changed afterwards by a
// refresh; see the note in 002_accounts.sql.
func (s *Store) UpsertAccount(ctx context.Context, sealer *secret.Sealer, acct Account, tok Tokens) (Account, error) {
	sealedAccess, err := sealer.Seal(tok.AccessToken)
	if err != nil {
		return Account{}, err
	}
	sealedRefresh, err := sealer.Seal(tok.RefreshToken)
	if err != nil {
		return Account{}, err
	}

	if acct.ID == "" {
		idBytes := make([]byte, 8)
		if _, err := rand.Read(idBytes); err != nil {
			return Account{}, fmt.Errorf("generate account id: %w", err)
		}
		acct.ID = hex.EncodeToString(idBytes)
	}
	now := time.Now().UTC()

	// Last in the priority list: adding an account must never silently change
	// which one is already serving.
	position := s.nextAccountPosition(ctx)

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO accounts (id, provider, email, account_uuid, access_token, refresh_token,
		                      expires_at, refresh_expires_at, created_at, last_refresh_at,
		                      position)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (provider, email) DO UPDATE SET
			access_token       = excluded.access_token,
			refresh_token      = excluded.refresh_token,
			expires_at         = excluded.expires_at,
			refresh_expires_at = excluded.refresh_expires_at,
			last_refresh_at    = excluded.last_refresh_at,
			last_error         = NULL,
			disabled_at        = NULL`,
		acct.ID, acct.Provider, acct.Email, acct.AccountUUID,
		sealedAccess, sealedRefresh,
		acct.ExpiresAt.UTC().Format(time.RFC3339), nullTime(acct.RefreshExpiresAt),
		now.Format(time.RFC3339), now.Format(time.RFC3339), position)
	if err != nil {
		return Account{}, fmt.Errorf("store account: %w", err)
	}

	return s.AccountByEmail(ctx, acct.Provider, acct.Email)
}

const accountColumns = `id, provider, email, account_uuid, expires_at, refresh_expires_at,
                        created_at, last_refresh_at, last_used_at, last_error, disabled_at,
                        quota_updated_at, quota_5h_util, quota_5h_reset, quota_5h_status,
                        quota_7d_util, quota_7d_reset, quota_7d_status, position, usage_json`

type scanner interface{ Scan(...any) error }

// nullTime renders an optional timestamp for SQLite: NULL when unknown, which
// is meaningfully different from "expired at the zero time".
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// parseRFC3339 is the lenient form: an unset column is the zero time, not an
// error, because none of these fields is required for the account to work.
func parseRFC3339(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}
	}
	return t
}

func scanAccount(sc scanner) (Account, error) {
	var (
		a                                                        Account
		expiresAt, createdAt                                     string
		refreshExpires, lastRefresh, lastUsed, lastErr, disabled sql.NullString
		quotaUpdated, reset5h, reset7d                           string
	)
	if err := sc.Scan(&a.ID, &a.Provider, &a.Email, &a.AccountUUID, &expiresAt, &refreshExpires,
		&createdAt, &lastRefresh, &lastUsed, &lastErr, &disabled,
		&quotaUpdated, &a.Quota.FiveHourUtil, &reset5h, &a.Quota.FiveHourStatus,
		&a.Quota.SevenDayUtil, &reset7d, &a.Quota.SevenDayStatus, &a.Position,
		&a.Quota.Detail); err != nil {
		return Account{}, err
	}
	a.Quota.UpdatedAt = parseRFC3339(quotaUpdated)
	a.Quota.FiveHourReset = parseRFC3339(reset5h)
	a.Quota.SevenDayReset = parseRFC3339(reset7d)
	a.ExpiresAt, _ = time.Parse(time.RFC3339, expiresAt)
	if refreshExpires.Valid {
		if t, err := time.Parse(time.RFC3339, refreshExpires.String); err == nil {
			a.RefreshExpiresAt = t
		}
	}
	a.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	a.LastError = lastErr.String
	if lastRefresh.Valid {
		if t, err := time.Parse(time.RFC3339, lastRefresh.String); err == nil {
			a.LastRefreshAt = &t
		}
	}
	if lastUsed.Valid {
		if t, err := time.Parse(time.RFC3339, lastUsed.String); err == nil {
			a.LastUsedAt = &t
		}
	}
	if disabled.Valid {
		if t, err := time.Parse(time.RFC3339, disabled.String); err == nil {
			a.DisabledAt = &t
		}
	}
	return a, nil
}

func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+accountColumns+` FROM accounts ORDER BY position, created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer rows.Close()

	var out []Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) Account(ctx context.Context, id string) (Account, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM accounts WHERE id = ?`, id)
	a, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrAccountNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("load account: %w", err)
	}
	return a, nil
}

func (s *Store) AccountByEmail(ctx context.Context, provider, email string) (Account, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE provider = ? AND email = ?`, provider, email)
	a, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrAccountNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("load account: %w", err)
	}
	return a, nil
}

// SetAccountOrder rewrites the priority list.
//
// It takes the whole order rather than a swap: two operators reordering at
// once would otherwise interleave their swaps into an order neither asked for,
// and the UI already knows the list it wants. Ids that no longer exist are
// ignored, and any account the caller did not mention keeps its place after
// the ones that were listed.
func (s *Store) SetAccountOrder(ctx context.Context, ids []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("reorder accounts: %w", err)
	}
	defer tx.Rollback()

	// Push everything out of the way first: position has no unique constraint,
	// but leaving stale values would put unlisted accounts among the listed.
	if _, err := tx.ExecContext(ctx,
		`UPDATE accounts SET position = position + ?`, len(ids)+1000); err != nil {
		return fmt.Errorf("reorder accounts: %w", err)
	}
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`UPDATE accounts SET position = ? WHERE id = ?`, i, id); err != nil {
			return fmt.Errorf("reorder accounts: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("reorder accounts: %w", err)
	}
	return nil
}

// nextAccountPosition puts a newly added account at the end of the list, so
// adding one never silently changes which account serves.
func (s *Store) nextAccountPosition(ctx context.Context) int {
	var n sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT MAX(position) FROM accounts`).Scan(&n); err != nil || !n.Valid {
		return 0
	}
	return int(n.Int64) + 1
}

// SetAccountQuota records what /api/oauth/usage reported for an account.
//
// Best-effort by design: this is a status poll, and failing to write a figure
// must never affect whether the account can serve.
func (s *Store) SetAccountQuota(ctx context.Context, id string, q AccountQuota) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET quota_updated_at = ?,
		                     quota_5h_util = ?, quota_5h_reset = ?, quota_5h_status = ?,
		                     quota_7d_util = ?, quota_7d_reset = ?, quota_7d_status = ?,
		                     usage_json = ?
		  WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339),
		q.FiveHourUtil, formatOrEmpty(q.FiveHourReset), q.FiveHourStatus,
		q.SevenDayUtil, formatOrEmpty(q.SevenDayReset), q.SevenDayStatus,
		q.Detail, id)
	if err != nil {
		return fmt.Errorf("record account usage: %w", err)
	}
	return nil
}

func formatOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// AccountTokens unseals the credentials for one account.
func (s *Store) AccountTokens(ctx context.Context, sealer *secret.Sealer, id string) (Tokens, error) {
	var sealedAccess, sealedRefresh []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT access_token, refresh_token FROM accounts WHERE id = ?`, id).
		Scan(&sealedAccess, &sealedRefresh)
	if errors.Is(err, sql.ErrNoRows) {
		return Tokens{}, ErrAccountNotFound
	}
	if err != nil {
		return Tokens{}, fmt.Errorf("load tokens: %w", err)
	}

	access, err := sealer.Open(sealedAccess)
	if err != nil {
		return Tokens{}, fmt.Errorf("unseal access token: %w", err)
	}
	refresh, err := sealer.Open(sealedRefresh)
	if err != nil {
		return Tokens{}, fmt.Errorf("unseal refresh token: %w", err)
	}
	return Tokens{AccessToken: access, RefreshToken: refresh}, nil
}

// UpdateAccountTokens replaces the credentials after a refresh. It deliberately
// takes no identity fields.
func (s *Store) UpdateAccountTokens(ctx context.Context, sealer *secret.Sealer, id string, tok Tokens, expiresAt, refreshExpiresAt time.Time) error {
	sealedAccess, err := sealer.Seal(tok.AccessToken)
	if err != nil {
		return err
	}
	sealedRefresh, err := sealer.Seal(tok.RefreshToken)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, `
		UPDATE accounts
		   SET access_token = ?, refresh_token = ?, expires_at = ?,
		       refresh_expires_at = COALESCE(?, refresh_expires_at),
		       last_refresh_at = ?, last_error = NULL
		 WHERE id = ?`,
		sealedAccess, sealedRefresh, expiresAt.UTC().Format(time.RFC3339),
		nullTime(refreshExpiresAt), now, id)
	if err != nil {
		return fmt.Errorf("update tokens: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAccountNotFound
	}
	return nil
}

func (s *Store) MarkAccountError(ctx context.Context, id, msg string) {
	_, _ = s.db.ExecContext(ctx, `UPDATE accounts SET last_error = ? WHERE id = ?`,
		strings.TrimSpace(msg), id)
}

func (s *Store) MarkAccountUsed(ctx context.Context, id string) {
	_, _ = s.db.ExecContext(ctx, `UPDATE accounts SET last_used_at = ?, last_error = NULL WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), id)
}

func (s *Store) DeleteAccount(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAccountNotFound
	}
	return nil
}
