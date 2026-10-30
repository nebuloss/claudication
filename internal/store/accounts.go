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

	"claudication/internal/live"
)

var ErrAccountNotFound = errors.New("account not found")

// Account is one logged-in upstream credential.
type Account struct {
	ID       string
	Provider string
	Email    string
	// Name is what the operator calls the account; empty when it is known
	// by its address. DisplayName is the one to show.
	Name        string
	AccountUUID string
	// Plan is the subscription, in the provider's word, as the sign-in said;
	// empty when it did not.
	Plan      string
	ExpiresAt time.Time
	// RefreshExpiresAt is when re-authorisation becomes unavoidable. Zero when
	// the provider did not say.
	RefreshExpiresAt time.Time
	CreatedAt        time.Time
	LastRefreshAt    *time.Time
	LastUsedAt       *time.Time
	LastError        string
	DisabledAt       *time.Time
	// RefreshDeadAt is when the refresh token was refused as invalid_grant.
	// Set, it means the account needs a human at a browser; nothing else will
	// bring it back.
	RefreshDeadAt *time.Time
	// Position is the operator's priority order, lowest first. The pool serves
	// from the top of this list, not from whichever account looks quietest.
	Position int
	// Quota is what the upstream last reported about this account's
	// subscription windows. Zero-valued until a response has been seen.
	Quota AccountQuota
	// ModelsOff are the models an operator turned off for this account, as
	// they were switched. See models.go; Serves is the question to ask.
	ModelsOff []string
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
	// Percentages in [0,100], or -1 when never fetched or not reported.
	// Unknown is deliberately not zero: an account we have not asked about is
	// unknown, not idle, and the two should not route the same way. Stored as
	// a missing row or a NULL (account_quota in schema.sql); -1 is this
	// struct's and the admin API's spelling of it.
	FiveHourUtil   float64
	FiveHourReset  time.Time
	FiveHourStatus string
	SevenDayUtil   float64
	SevenDayReset  time.Time
	SevenDayStatus string
	// Detail is the raw `limits` JSON, passed through to the UI untouched.
	Detail string
}

// Known reports whether the upstream has told us about this account's
// five-hour or weekly window: what the pool and the subscription shares
// reckon with.
func (q AccountQuota) Known() bool { return q.FiveHourUtil >= 0 || q.SevenDayUtil >= 0 }

// Reported reports whether the upstream has told us anything about this
// account's quota: a window, or rows of its own in the detail. Google's
// quota is per model and has no window (internal/provider/google, usage.go),
// so its accounts are Reported and never Known — shown on the accounts
// screen, and left out of what reckons in windows.
func (q AccountQuota) Reported() bool { return q.Known() || q.Detail != "" }

// AtLimit is the percentage at which an account counts as out of room. Not
// 100: the figure is a poll rather than a live reading, and an account at
// 99.5% has nothing useful left before the next poll lands.
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

// QuotaWindow is one rolling limit as a provider reports it: by its length,
// not by a name. The same fields as provider.Window, which converts to it
// directly; store and provider sit on one layer and cannot share the type.
type QuotaWindow struct {
	Seconds     int64
	UsedPercent float64
	ResetAt     time.Time
	Status      string
}

// The window lengths the quota has columns for, in seconds: the one
// definition a provider reporting those windows names them by. (The admin UI
// has its own, in web/src/ui/domain/quota.ts, being TypeScript.)
const (
	FiveHourSeconds = 5 * 60 * 60
	SevenDaySeconds = 7 * 24 * 60 * 60
)

// near reports whether a window is the given length, give or take a tenth: a
// provider that reports in minutes, or rounds, is still describing the same
// window.
func near(seconds, want int64) bool {
	d := seconds - want
	if d < 0 {
		d = -d
	}
	return d*10 <= want
}

// QuotaFromWindows is a provider's windows as the quota columns hold them.
//
// By length, never by position: the 5-hour columns take the window about five
// hours long and the 7-day columns the one about a week long, and any other
// window is left to detail, where the accounts screen still shows it. A
// window not reported stays unknown (-1), as it does for a poll that named
// none.
//
// limited is the provider saying the account may not serve now. It closes the
// windows that are full, or both columns when none is — the pool reads a
// closed window as out of room, which is what limited means — so the account
// is skipped without the figures having to agree.
func QuotaFromWindows(at time.Time, windows []QuotaWindow, limited bool, detail string) AccountQuota {
	q := AccountQuota{UpdatedAt: at, FiveHourUtil: -1, SevenDayUtil: -1, Detail: detail}
	for _, w := range windows {
		status := w.Status
		if status == "" {
			status = "allowed"
		}
		switch {
		case near(w.Seconds, FiveHourSeconds):
			q.FiveHourUtil, q.FiveHourReset, q.FiveHourStatus = w.UsedPercent, w.ResetAt.UTC(), status
		case near(w.Seconds, SevenDaySeconds):
			q.SevenDayUtil, q.SevenDayReset, q.SevenDayStatus = w.UsedPercent, w.ResetAt.UTC(), status
		}
	}
	if limited {
		closed := false
		if q.FiveHourUtil >= 100 {
			q.FiveHourStatus, closed = "limit_reached", true
		}
		if q.SevenDayUtil >= 100 {
			q.SevenDayStatus, closed = "limit_reached", true
		}
		if !closed {
			q.FiveHourStatus, q.SevenDayStatus = "limit_reached", "limit_reached"
		}
	}
	return q
}

func (a Account) Disabled() bool { return a.DisabledAt != nil }

// DisplayName is what the account is called: its name, or its address when
// it has none. The names ledger holds the same (rememberAccountName).
func (a Account) DisplayName() string {
	if a.Name != "" {
		return a.Name
	}
	return a.Email
}

// RefreshDead reports whether the refresh token has been refused for good.
func (a Account) RefreshDead() bool { return a.RefreshDeadAt != nil }

// NeedsReauth is the operator-facing question: can this account come back on
// its own? A dead refresh token cannot, and neither can one whose refresh
// window has already closed.
func (a Account) NeedsReauth() bool {
	if a.RefreshDead() {
		return true
	}
	d, known := a.RefreshWindow()
	return known && d <= 0
}

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

// sealTokens seals both halves of a credential, the one way either is written.
func (s *Store) sealTokens(tok Tokens) (access, refresh []byte, err error) {
	if access, err = s.sealer.Seal(tok.AccessToken); err != nil {
		return nil, nil, fmt.Errorf("seal access token: %w", err)
	}
	if refresh, err = s.sealer.Seal(tok.RefreshToken); err != nil {
		return nil, nil, fmt.Errorf("seal refresh token: %w", err)
	}
	return access, refresh, nil
}

// UpsertAccount stores a freshly authorised account, or replaces the tokens of
// one that already exists for this provider, email and account id.
//
// Identity (email, uuid) is set at login and never changed afterwards by a
// refresh; see the note on accounts in schema.sql.
func (s *Store) UpsertAccount(ctx context.Context, acct Account, tok Tokens) (Account, error) {
	defer s.changed(live.Accounts)
	sealedAccess, sealedRefresh, err := s.sealTokens(tok)
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
	// which one is already serving. The position is computed inside the INSERT
	// rather than by a query before it, so two accounts added at once cannot
	// take the same place. A reconnect keeps its place: position is not in the
	// update list.
	if err := s.inTx(ctx, "store account", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO accounts (id, provider, email, account_uuid, plan, access_token, refresh_token,
			                      expires_at, refresh_expires_at, created_at, last_refresh_at,
			                      position)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			        (SELECT COALESCE(MAX(position) + 1, 0) FROM accounts))
			ON CONFLICT (provider, email, account_uuid) DO UPDATE SET
				plan               = COALESCE(NULLIF(excluded.plan, ''), plan),
				access_token       = excluded.access_token,
				refresh_token      = excluded.refresh_token,
				expires_at         = excluded.expires_at,
				refresh_expires_at = excluded.refresh_expires_at,
				last_refresh_at    = excluded.last_refresh_at,
				last_error         = '',
				disabled_at        = NULL,
				-- A successful consent flow is exactly the human intervention a
				-- dead refresh token was waiting for.
				refresh_dead_at    = NULL`,
			acct.ID, acct.Provider, acct.Email, acct.AccountUUID, acct.Plan,
			sealedAccess, sealedRefresh,
			ms(acct.ExpiresAt), nullMS(acct.RefreshExpiresAt), ms(now), ms(now)); err != nil {
			return fmt.Errorf("store account: %w", err)
		}
		// The row that took the write, which is not acct.ID on a reconnect.
		var id string
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM accounts WHERE provider = ? AND email = ? AND account_uuid = ?`,
			acct.Provider, acct.Email, acct.AccountUUID).Scan(&id); err != nil {
			return fmt.Errorf("store account: %w", err)
		}
		return rememberAccountName(ctx, tx, id)
	}); err != nil {
		return Account{}, err
	}

	return s.AccountByIdentity(ctx, acct.Provider, acct.AccountUUID, acct.Email)
}

// accountColumns is every column an Account is read from, in scanAccount's
// order, selected FROM accountsWithQuota. The quota has its own table, joined
// rather than subqueried column by column; it shares no column name with
// accounts, so the statements' own WHERE and ORDER BY stay unqualified.
const accountColumns = `accounts.id, provider, email, name, account_uuid, plan, expires_at, refresh_expires_at,
                        created_at, last_refresh_at, last_used_at, last_error, disabled_at,
                        refresh_dead_at,
                        q.updated_at, q.five_hour_util, q.five_hour_reset, COALESCE(q.five_hour_status, ''),
                        q.seven_day_util, q.seven_day_reset, COALESCE(q.seven_day_status, ''),
                        position, COALESCE(q.detail, ''),
                        COALESCE((SELECT group_concat(model, char(31)) FROM account_models_off o
                                   WHERE o.account_id = accounts.id), '')`

const accountsWithQuota = `accounts LEFT JOIN account_quota q ON q.account_id = accounts.id`

func scanAccount(sc scanner) (Account, error) {
	var (
		a                               Account
		expiresAt, createdAt            int64
		refreshExpires, lastRefresh     sql.NullInt64
		lastUsed, disabled, refreshDead sql.NullInt64
		quotaUpdated, reset5h, reset7d  sql.NullInt64
		util5h, util7d                  sql.NullFloat64
		modelsOff                       string
	)
	if err := sc.Scan(&a.ID, &a.Provider, &a.Email, &a.Name, &a.AccountUUID, &a.Plan, &expiresAt, &refreshExpires,
		&createdAt, &lastRefresh, &lastUsed, &a.LastError, &disabled, &refreshDead,
		&quotaUpdated, &util5h, &reset5h, &a.Quota.FiveHourStatus,
		&util7d, &reset7d, &a.Quota.SevenDayStatus, &a.Position,
		&a.Quota.Detail, &modelsOff); err != nil {
		return Account{}, err
	}
	a.ModelsOff = splitModels(modelsOff)
	a.Quota.UpdatedAt = fromNullMS(quotaUpdated)
	a.Quota.FiveHourUtil = utilOrUnknown(util5h)
	a.Quota.SevenDayUtil = utilOrUnknown(util7d)
	a.Quota.FiveHourReset = fromNullMS(reset5h)
	a.Quota.SevenDayReset = fromNullMS(reset7d)
	a.ExpiresAt = fromMS(expiresAt)
	a.RefreshExpiresAt = fromNullMS(refreshExpires)
	a.CreatedAt = fromMS(createdAt)
	a.LastRefreshAt = optionalMS(lastRefresh)
	a.LastUsedAt = optionalMS(lastUsed)
	a.DisabledAt = optionalMS(disabled)
	a.RefreshDeadAt = optionalMS(refreshDead)
	return a, nil
}

// utilOrUnknown is a stored percentage, or -1 for no row or a window the
// endpoint did not report.
func utilOrUnknown(v sql.NullFloat64) float64 {
	if !v.Valid {
		return -1
	}
	return v.Float64
}

// utilOrNull is the reverse: anything below zero is not a percentage.
func utilOrNull(v float64) any {
	if v < 0 {
		return nil
	}
	return v
}

func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+accountColumns+` FROM `+accountsWithQuota+` ORDER BY position, created_at, accounts.id`)
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
	row := s.db.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM `+accountsWithQuota+` WHERE accounts.id = ?`, id)
	a, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrAccountNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("load account: %w", err)
	}
	return a, nil
}

// AccountByIdentity is the stored account a sign-in names: the provider's own
// account id where it gave one, and the address otherwise. The id first,
// because one ChatGPT sign-in reaches a personal workspace and a Team one under
// the same address and they are two accounts; and an account renamed upstream
// is still itself. An account stored before its provider's id was known — an
// Anthropic one from an older release — is found by address, as it always
// was.
func (s *Store) AccountByIdentity(ctx context.Context, provider, uuid, email string) (Account, error) {
	if uuid != "" {
		row := s.db.QueryRowContext(ctx,
			`SELECT `+accountColumns+` FROM `+accountsWithQuota+` WHERE provider = ? AND account_uuid = ?
			  ORDER BY position LIMIT 1`, provider, uuid)
		a, err := scanAccount(row)
		if err == nil {
			return a, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Account{}, fmt.Errorf("load account: %w", err)
		}
		// Not yet known by its id: an account from before ids were stored,
		// at the same address, is the same one.
		row = s.db.QueryRowContext(ctx,
			`SELECT `+accountColumns+` FROM `+accountsWithQuota+`
			  WHERE provider = ? AND email = ? COLLATE NOCASE AND account_uuid = ''`, provider, email)
		a, err = scanAccount(row)
		if errors.Is(err, sql.ErrNoRows) {
			return Account{}, ErrAccountNotFound
		}
		if err != nil {
			return Account{}, fmt.Errorf("load account: %w", err)
		}
		return a, nil
	}
	return s.AccountByEmail(ctx, provider, email)
}

// AccountByEmail is the account a provider knows by this address, the first
// in the priority list when several workspaces share it. An address is
// matched whatever its case, as sign-in compares them (sameAccount): one
// that came back capitalised otherwise was a second account.
func (s *Store) AccountByEmail(ctx context.Context, provider, email string) (Account, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+accountColumns+` FROM `+accountsWithQuota+` WHERE provider = ? AND email = ? COLLATE NOCASE
		  ORDER BY position LIMIT 1`, provider, email)
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
	defer s.changed(live.Accounts)
	return s.inTx(ctx, "reorder accounts", func(tx *sql.Tx) error {
		// Push everything out of the way first: position has no unique
		// constraint, but leaving stale values would put unlisted accounts among
		// the listed.
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
		return nil
	})
}

// SetAccountQuota records what /api/oauth/usage reported for an account.
//
// Best-effort by design: this is a status poll, and failing to write a figure
// must never affect whether the account can serve.
//
// A poll that lands as the account is deleted writes nothing: the SELECT finds
// no account, where an INSERT would fail the foreign key.
func (s *Store) SetAccountQuota(ctx context.Context, id string, q AccountQuota) error {
	defer s.changed(live.Quota)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO account_quota (account_id, updated_at,
		                            five_hour_util, five_hour_reset, five_hour_status,
		                            seven_day_util, seven_day_reset, seven_day_status, detail)
		 SELECT id, ?, ?, ?, ?, ?, ?, ?, ? FROM accounts WHERE id = ?
		 ON CONFLICT (account_id) DO UPDATE SET
		   updated_at = excluded.updated_at,
		   five_hour_util = excluded.five_hour_util, five_hour_reset = excluded.five_hour_reset,
		   five_hour_status = excluded.five_hour_status,
		   seven_day_util = excluded.seven_day_util, seven_day_reset = excluded.seven_day_reset,
		   seven_day_status = excluded.seven_day_status, detail = excluded.detail`,
		ms(time.Now()),
		utilOrNull(q.FiveHourUtil), nullMS(q.FiveHourReset), q.FiveHourStatus,
		utilOrNull(q.SevenDayUtil), nullMS(q.SevenDayReset), q.SevenDayStatus,
		q.Detail, id)
	if err != nil {
		return fmt.Errorf("record account usage: %w", err)
	}
	return nil
}

// SetAccountPlan records an account's plan as its provider names it now,
// for a plan that changed after sign-in.
func (s *Store) SetAccountPlan(ctx context.Context, id, plan string) error {
	defer s.changed(live.Accounts)
	if _, err := s.db.ExecContext(ctx, `UPDATE accounts SET plan = ? WHERE id = ?`, plan, id); err != nil {
		return fmt.Errorf("record account plan: %w", err)
	}
	return nil
}

// AccountTokens unseals the credentials for one account.
func (s *Store) AccountTokens(ctx context.Context, id string) (Tokens, error) {
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

	access, err := s.sealer.Open(sealedAccess)
	if err != nil {
		return Tokens{}, fmt.Errorf("unseal access token: %w", err)
	}
	refresh, err := s.sealer.Open(sealedRefresh)
	if err != nil {
		return Tokens{}, fmt.Errorf("unseal refresh token: %w", err)
	}
	return Tokens{AccessToken: access, RefreshToken: refresh}, nil
}

// UpdateAccountTokens replaces the credentials after a refresh. It deliberately
// takes no identity fields.
func (s *Store) UpdateAccountTokens(ctx context.Context, id string, tok Tokens, expiresAt, refreshExpiresAt time.Time) error {
	defer s.changed(live.Accounts)
	sealedAccess, sealedRefresh, err := s.sealTokens(tok)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE accounts
		   SET access_token = ?, refresh_token = ?, expires_at = ?,
		       refresh_expires_at = COALESCE(?, refresh_expires_at),
		       last_refresh_at = ?, last_error = ''
		 WHERE id = ?`,
		sealedAccess, sealedRefresh, ms(expiresAt),
		nullMS(refreshExpiresAt), ms(time.Now()), id)
	if err != nil {
		return fmt.Errorf("update tokens: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAccountNotFound
	}
	return nil
}

// RenewAccount stores the credentials of a fresh consent flow on the account
// with the given id: the reconnect, as opposed to UpsertAccount's add.
//
// By id rather than by email, so the account keeps everything that is not a
// credential — its place in the priority list, its history, its id — even when
// the provider now reports a different address for it. The caller has already
// established that the tokens belong to this account.
//
// It clears what a consent flow repairs: the error, the dead refresh token,
// and a pause, as UpsertAccount does. The refresh token's lifetime is replaced
// outright rather than kept when absent, because the old one described the old
// grant.
func (s *Store) RenewAccount(ctx context.Context, id string, acct Account, tok Tokens) (Account, error) {
	defer s.changed(live.Accounts)
	sealedAccess, sealedRefresh, err := s.sealTokens(tok)
	if err != nil {
		return Account{}, err
	}
	if err := s.inTx(ctx, "renew account", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE accounts
			   SET access_token = ?, refresh_token = ?, expires_at = ?,
			       refresh_expires_at = ?, last_refresh_at = ?,
			       email = COALESCE(NULLIF(?, ''), email),
			       account_uuid = COALESCE(NULLIF(?, ''), account_uuid),
			       plan = COALESCE(NULLIF(?, ''), plan),
			       last_error = '', disabled_at = NULL, refresh_dead_at = NULL
			 WHERE id = ?`,
			sealedAccess, sealedRefresh, ms(acct.ExpiresAt),
			nullMS(acct.RefreshExpiresAt), ms(time.Now()), acct.Email, acct.AccountUUID, acct.Plan, id)
		if err != nil {
			return fmt.Errorf("renew account: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrAccountNotFound
		}
		// A new address is a rename of an unnamed account, and its history
		// follows it; a named one keeps the name it was given.
		return rememberAccountName(ctx, tx, id)
	}); err != nil {
		return Account{}, err
	}
	return s.Account(ctx, id)
}

// MarkAccountError records why the account last failed.
//
// It lets the next MarkAccountUsed write at once, whatever its once-a-minute
// limit says: that write is what clears the error, and an account that has
// recovered must not show a failure for up to a minute more.
func (s *Store) MarkAccountError(ctx context.Context, id, msg string) {
	defer s.changed(live.Accounts)
	_, _ = s.db.ExecContext(ctx, `UPDATE accounts SET last_error = ? WHERE id = ?`,
		strings.TrimSpace(msg), id)
	s.accountsUsed.forget(id)
}

// MarkAccountUsed records a success: when, and that there is no error any
// more. At most once a minute per account (see lastUse), unless an error was
// marked since.
func (s *Store) MarkAccountUsed(ctx context.Context, id string) {
	defer s.changed(live.Accounts)
	now := time.Now()
	if !s.accountsUsed.due(id, now) {
		return
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE accounts SET last_used_at = ?, last_error = '' WHERE id = ?`,
		ms(now), id); err != nil {
		// Not written, so not a write to wait a minute after: this is also
		// what clears an error the account has recovered from.
		s.accountsUsed.forget(id)
	}
}

// MarkRefreshDead records that the refresh token was refused for good.
//
// Idempotent, and it keeps the first time rather than the most recent, because
// "since when" is the useful figure when an operator finally looks.
func (s *Store) MarkRefreshDead(ctx context.Context, id, detail string) error {
	defer s.changed(live.Accounts)
	_, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET refresh_dead_at = COALESCE(refresh_dead_at, ?), last_error = ?
		  WHERE id = ?`,
		ms(time.Now()), detail, id)
	if err != nil {
		return fmt.Errorf("mark refresh dead: %w", err)
	}
	s.accountsUsed.forget(id) // it set last_error, as MarkAccountError does
	return nil
}

// SetAccountDisabled takes an account out of rotation, or puts it back.
//
// The column, the Disabled() helper, the pool's check and the UI badge all
// existed already; nothing ever wrote the value, so the only way to stop using
// an account was to delete it and destroy its credentials. That is a poor
// answer to "not this one this week" — reconnecting means going through the
// browser flow again.
//
// Reconnecting an account clears this, which is the right behaviour: an
// operator who goes through the consent flow again means to use it.
func (s *Store) SetAccountDisabled(ctx context.Context, id string, disabled bool) error {
	defer s.changed(live.Accounts)
	var res sql.Result
	var err error
	if disabled {
		res, err = s.db.ExecContext(ctx,
			`UPDATE accounts SET disabled_at = ? WHERE id = ?`, ms(time.Now()), id)
	} else {
		res, err = s.db.ExecContext(ctx,
			`UPDATE accounts SET disabled_at = NULL WHERE id = ?`, id)
	}
	if err != nil {
		return fmt.Errorf("set account disabled: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAccountNotFound
	}
	return nil
}

// SetAccountName names an account, or with "" gives it back its address as
// its name. Names need not be unique: an account's id is what identifies it,
// and two seats an operator calls the same are the operator's to tell apart.
// The ledger is written in the same transaction, so its history is relabelled
// at once.
func (s *Store) SetAccountName(ctx context.Context, id, name string) error {
	defer s.changed(live.Accounts)
	name, err := CleanName(name)
	if err != nil {
		return err
	}
	return s.inTx(ctx, "name account", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE accounts SET name = ? WHERE id = ?`, name, id)
		if err != nil {
			return fmt.Errorf("name account: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrAccountNotFound
		}
		return rememberAccountName(ctx, tx, id)
	})
}

func (s *Store) DeleteAccount(ctx context.Context, id string) error {
	defer s.changed(live.Accounts)
	res, err := s.db.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAccountNotFound
	}
	s.accountsUsed.forget(id)
	return nil
}
