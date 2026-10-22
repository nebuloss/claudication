package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The migrations that built every database up to version 28, kept here only
// to build one: the upgrade starts from what they left behind, and nothing
// else can say what that was.
//
//go:embed testdata/migrations-v28/*.sql
var v28Migrations embed.FS

// buildOld creates a database the way the releases that shipped 001-028 did,
// up to and including version upTo: each file in its own transaction with its
// version recorded, through the same driver. dsnString is how it is opened, so
// a file from before auto_vacuum was set can be built too.
func buildOld(t *testing.T, dsnString string, upTo int) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", dsnString)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`); err != nil {
		t.Fatal(err)
	}
	entries, err := v28Migrations.ReadDir("testdata/migrations-v28")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		v, _ := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if v > upTo {
			break
		}
		body, err := v28Migrations.ReadFile(path.Join("testdata/migrations-v28", name))
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations VALUES (?, '2026-01-01T00:00:00Z')`, v); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// v28 is the fixture: rows of every shape the version-28 code wrote, in the
// forms it wrote them — RFC3339 at seconds, nine-digit stamps, unix seconds,
// -1 and the empty string for an unknown quota, an empty key_id for the
// gateway's model roles.
type v28 struct {
	keyKept, keyRenamed      string // two live keys; the second renamed after use
	account, accountNever    string // quota fetched, quota never fetched
	created                  time.Time
	events, rejected, titled int
	tokens                   int64
	errors                   int // by the old definition
	// stamps are the events' at, as written, oldest first.
	stamps []string
}

func fillV28(t *testing.T, db *sql.DB) v28 {
	t.Helper()
	f := v28{keyKept: "k1aaaaaaaaaaaaaa", keyRenamed: "k2bbbbbbbbbbbbbb",
		account: "a1aaaaaaaaaaaaaa", accountNever: "a2bbbbbbbbbbbbbb",
		created: time.Date(2026, 9, 1, 8, 30, 15, 0, time.UTC)}
	rfc := f.created.Format(time.RFC3339)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO api_keys (id, name, prefix, hash, created_at, last_used_at, rpm_limit, token_budget,
	                            rate_period_s, sealed_secret)
	      VALUES (?, 'laptop', 'aaaaaaaaaaaa', 'h1', ?, ?, 120, 5000, 3600, x'0102'),
	             (?, 'after', 'bbbbbbbbbbbb', 'h2', ?, NULL, 0, 0, 60, NULL)`,
		f.keyKept, rfc, rfc, f.keyRenamed, rfc)
	exec(`INSERT INTO api_key_models_off VALUES (?, 'claude-opus-4-1', ?)`, f.keyKept, rfc)
	exec(`INSERT INTO model_roles VALUES ('', 'claude-large', '', '2026-10-15T10:00:00.500000000Z'),
	                                     ('', 'claude-fast', 'claude-haiku-4-5', '2026-10-15T10:00:00.000000000Z'),
	                                     (?, 'claude-small', 'claude-haiku-4-5', '2026-10-15T10:00:00.123456789Z'),
	                                     ('deadbeefdeadbeef', 'claude-small', 'x', '2026-10-15T10:00:00.000000000Z')`,
		f.keyKept)
	exec(`INSERT INTO accounts (id, provider, email, account_uuid, access_token, refresh_token, expires_at,
	                            created_at, last_refresh_at, last_used_at, last_error, disabled_at,
	                            refresh_expires_at, quota_updated_at, quota_5h_util, quota_5h_reset,
	                            quota_5h_status, quota_7d_util, quota_7d_reset, quota_7d_status, position,
	                            usage_json, refresh_dead_at)
	      VALUES (?, 'anthropic', 'one@example.com', 'u1', x'00', x'00', ?, ?, ?, ?, 'boom', NULL,
	              '2026-11-01T00:00:00Z', '2026-10-19T11:58:00Z', 42.5, '2026-10-19T15:00:00Z',
	              'allowed', -1, '', '', 0, '[{"x":1}]', '2026-10-18T00:00:00Z'),
	             (?, 'anthropic', 'two@example.com', '', x'00', x'00', ?, ?, NULL, NULL, NULL, ?,
	              NULL, '', -1, '', '', -1, '', '', 1, '', NULL)`,
		f.account, rfc, rfc, rfc, rfc, f.accountNever, rfc, rfc, rfc)
	exec(`INSERT INTO account_models_off VALUES (?, 'claude-opus-4-1', ?)`, f.account, rfc)

	// Events: a key's history before and after its rename, a key since
	// deleted, refusals, the gateway's own titling, and failures the
	// classifier left without a code.
	at := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	add := func(keyID, keyName, accountID, email, reqPath string, status int, rejected bool, errText, code string, tokens int) {
		t.Helper()
		at = at.Add(1500 * time.Microsecond) // sub-millisecond, as stamps were
		stamp := at.Format("2006-01-02T15:04:05.000000000Z07:00")
		f.stamps = append(f.stamps, stamp)
		exec(`INSERT INTO usage_events (at, key_id, key_name, account_id, account_email, model, path, status,
		                                streaming, input_tokens, output_tokens, duration_ms, error,
		                                conversation_id, client, rejected, ip, error_code, first_token_ms,
		                                virtual_model)
		      VALUES (?, ?, ?, ?, ?, 'claude-opus-5', ?, ?, 1, ?, ?, 900, ?, 'chat-1', 'Claude Code', ?, '10.0.0.1',
		              ?, 120, 'claude-large')`,
			stamp, keyID, keyName, accountID, email, reqPath, status,
			tokens, tokens*2, errText, rejected, code)
		f.events++
		if rejected {
			f.rejected++
			return
		}
		if reqPath == "/internal/title" {
			f.titled++
		}
		f.tokens += int64(tokens * 3)
		if status == 0 || status >= 400 || errText != "" {
			f.errors++
		}
	}
	for i := 0; i < 5; i++ {
		add(f.keyRenamed, "before", f.account, "one@example.com", "/v1/messages", 200, false, "", "", 10)
	}
	add(f.keyKept, "laptop", f.account, "one@example.com", "/v1/messages", 429, false, "slow", "rate_limit_error", 0)
	add(f.keyKept, "laptop", f.account, "one@example.com", "/v1/messages", 500, false, "", "", 0)
	add(f.keyKept, "laptop", f.accountNever, "two@example.com", "/v1/messages", 0, false, "", "", 0)
	add("gone0000gone0000", "old-name", f.account, "one@example.com", "/v1/messages", 200, false, "", "", 7)
	add("titler00titler00", "gateway (internal)", f.account, "one@example.com", "/internal/title", 200, false, "", "", 3)
	add("", "", "", "", "/v1/messages", 401, true, "missing API key", "authentication_error", 0)
	add("", "", "", "", "/v1/messages", 401, true, "missing API key", "authentication_error", 0)

	exec(`INSERT INTO chat_titles VALUES ('chat-1', 'Refactor', 'claude-haiku-4-5', '2026-10-19T09:00:00.250000000Z', 3, 2, 1)`)
	exec(`INSERT INTO admin VALUES (1, 'scrypt$32768$aa$bb', ?, ?)`, rfc, rfc)
	exec(`INSERT INTO settings VALUES ('api.openai', 'off', 1760000000)`)
	future := time.Now().Add(10 * time.Minute).UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
	exec(`INSERT INTO login_links VALUES ('live', ?, ?), ('stale', '2026-01-01T00:00:00.000000000Z',
	      '2026-01-01T00:15:00.000000000Z')`, future, future)
	return f
}

// upgraded builds the fixture and opens it with this release.
func upgraded(t *testing.T, oldDSN func(string) string) (*Store, v28) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "v28.db")
	db := buildOld(t, oldDSN(p), 28)
	f := fillV28(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(context.Background(), p, testSealer(t))
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st, f
}

// shape is everything SQLite says about a database's schema, per object, in a
// form two databases can be compared by.
func shape(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	rowsOf := func(q string) []string {
		t.Helper()
		rows, err := db.Query(q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		var out []string
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprint(vals...))
		}
		return out
	}
	out := map[string][]string{}
	tables := rowsOf(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	out["tables"] = tables
	out["sqlite_master"] = rowsOf(`SELECT type, name, tbl_name, sql FROM sqlite_master
		WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	for _, tbl := range tables {
		out[tbl+" table_list"] = rowsOf(`SELECT schema, name, type, ncol, wr, strict FROM pragma_table_list('` + tbl + `')`)
		out[tbl+" table_xinfo"] = rowsOf(`SELECT * FROM pragma_table_xinfo('` + tbl + `')`)
		out[tbl+" foreign_key_list"] = rowsOf(`SELECT * FROM pragma_foreign_key_list('` + tbl + `')`)
		indexes := rowsOf(`SELECT name FROM pragma_index_list('` + tbl + `') ORDER BY name`)
		out[tbl+" index_list"] = rowsOf(`SELECT name, "unique", origin, partial FROM pragma_index_list('` + tbl + `') ORDER BY name`)
		for _, ix := range indexes {
			out[tbl+" "+ix+" index_xinfo"] = rowsOf(`SELECT * FROM pragma_index_xinfo('` + ix + `')`)
		}
	}
	return out
}

// An upgraded database and a fresh one must be the same database: the same
// tables, columns, types, defaults, keys, indexes and foreign keys, down to
// the text SQLite keeps of each. A difference is a database whose next
// migration was written against a schema it does not have.
func TestUpgradeMatchesFreshSchema(t *testing.T) {
	st, _ := upgraded(t, dsn)
	fresh := newTestStore(t)

	got, want := shape(t, st.db), shape(t, fresh.db)
	for k, w := range want {
		if g := got[k]; !reflect.DeepEqual(g, w) {
			t.Errorf("%s differs:\n upgraded %q\n    fresh %q", k, g, w)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s exists only in the upgraded database: %q", k, got[k])
		}
	}

	var version, rows int
	if err := st.db.QueryRow(`SELECT MAX(version), COUNT(*) FROM schema_migrations`).Scan(&version, &rows); err != nil {
		t.Fatal(err)
	}
	// One row per step taken from 28: the rows before it went with 029.
	if version != schemaVersion || rows != schemaVersion-oldestUpgradable {
		t.Errorf("upgraded to version %d in %d rows, want %d in %d", version, rows, schemaVersion,
			schemaVersion-oldestUpgradable)
	}
	var mode int
	if err := st.db.QueryRow(`PRAGMA auto_vacuum`).Scan(&mode); err != nil || mode != 2 {
		t.Errorf("auto_vacuum = %d, %v; want 2", mode, err)
	}
}

// A file from before auto_vacuum was set is VACUUMed by the upgrade, which is
// what applies the mode to it.
func TestUpgradeAppliesAutoVacuum(t *testing.T) {
	st, _ := upgraded(t, func(p string) string {
		return "file:" + p + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	})
	if mode, err := st.AutoVacuum(context.Background()); err != nil || mode != 2 {
		t.Errorf("auto_vacuum after the upgrade = %d, %v; want 2", mode, err)
	}
}

// Everything the version-28 code wrote reads back through this release's
// store as it did through that one's.
func TestUpgradeKeepsTheData(t *testing.T) {
	st, f := upgraded(t, dsn)
	ctx := context.Background()

	count := func(q string) int {
		t.Helper()
		var n int
		if err := st.db.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	if n := count(`SELECT COUNT(*) FROM usage_events`); n != f.events {
		t.Errorf("%d events, want %d", n, f.events)
	}
	for kind, want := range map[int]int{kindRelayed: f.events - f.rejected - f.titled, kindRejected: f.rejected, kindInternal: f.titled} {
		if n := count(`SELECT COUNT(*) FROM usage_events WHERE kind = ` + strconv.Itoa(kind)); n != want {
			t.Errorf("kind %d: %d rows, want %d", kind, n, want)
		}
	}
	// A failure with no text had no code; every failure has one now.
	if n := count(`SELECT COUNT(*) FROM usage_events WHERE (status = 0 OR status >= 400 OR error <> '') <> (error_code <> '')`); n != 0 {
		t.Errorf("%d rows whose error_code disagrees with their outcome", n)
	}
	if n := count(`SELECT COUNT(*) FROM usage_events WHERE status = 0 AND error_code = 'no_answer'`); n != 1 {
		t.Errorf("the status-0 row was not filed as no_answer")
	}

	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	totals, err := st.Totals(ctx, since)
	if err != nil {
		t.Fatal(err)
	}
	if totals.Requests != int64(f.events-f.rejected) || totals.Errors != int64(f.errors) ||
		totals.InputTokens+totals.OutputTokens != f.tokens {
		t.Errorf("totals %+v; want %d requests, %d errors, %d tokens",
			totals, f.events-f.rejected, f.errors, f.tokens)
	}

	// Names: a renamed key shows its current name across its history, a
	// deleted one the name it last had.
	rep, err := st.Usage(ctx, since)
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{}
	for _, b := range rep.ByKey {
		labels[b.ID] = b.Label
	}
	if labels[f.keyRenamed] != "after" || labels["gone0000gone0000"] != "old-name" || labels[f.keyKept] != "laptop" {
		t.Errorf("key labels %v", labels)
	}
	events, _, err := st.RecentUsage(ctx, 100, UsageCursor{}, RequestFilter{})
	if err != nil || len(events) != f.events {
		t.Fatalf("RecentUsage = %d, %v", len(events), err)
	}
	newest := events[0]
	if !newest.Rejected || newest.Status != 401 || newest.KeyName != "" {
		t.Errorf("newest = %+v, want a refusal", newest)
	}
	for _, e := range events {
		if e.KeyID == f.keyRenamed && (e.KeyName != "after" || e.AccountEmail != "one@example.com") {
			t.Errorf("renamed key's row reads %q/%q", e.KeyName, e.AccountEmail)
		}
		if e.VirtualModel != "claude-large" || e.FirstToken != 120*time.Millisecond {
			t.Errorf("row %d lost alias or first token: %+v", e.ID, e)
		}
		if e.At.Nanosecond()%int(time.Millisecond) != 0 {
			t.Errorf("row %d at %v is finer than a millisecond", e.ID, e.At)
		}
	}
	// An old text cursor still pages: from the third row as the old release
	// named it, to the nanosecond, the rest.
	third := events[2]
	old := f.stamps[f.events-3] + "|" + strconv.FormatInt(third.ID, 10)
	cur, ok := ParseUsageCursor(old)
	if !ok {
		t.Fatalf("old cursor %q did not parse", old)
	}
	rest, _, err := st.RecentUsage(ctx, 100, cur, RequestFilter{})
	if err != nil || len(rest) != f.events-3 || rest[0].ID != events[3].ID {
		t.Errorf("old cursor paged to %d rows, %v; want the %d after it", len(rest), err, f.events-3)
	}

	chats, err := st.Chats(ctx, since, 10)
	if err != nil || len(chats.Chats) != 1 {
		t.Fatalf("chats %+v, %v", chats, err)
	}
	// The latest relayed row is the deleted key's; the titler's is later and
	// does not count.
	if c := chats.Chats[0]; c.Title != "Refactor" || !c.TitleManual || c.KeyName != "old-name" {
		t.Errorf("chat %+v: the title, or the latest relayed key, did not survive", c)
	}

	// Keys: times, limits, the kept secret, the switches.
	keys, err := st.ListKeys(ctx)
	if err != nil || len(keys) != 2 {
		t.Fatalf("keys %+v, %v", keys, err)
	}
	k, _ := st.Key(ctx, f.keyKept)
	if !k.CreatedAt.Equal(f.created) || k.LastUsedAt == nil || !k.LastUsedAt.Equal(f.created) ||
		k.RPMLimit != 120 || k.RatePeriod != time.Hour || k.TokenBudget != 5000 || !k.SecretKept ||
		len(k.ModelsOff) != 1 {
		t.Errorf("key %+v", k)
	}
	if k, _ := st.Key(ctx, f.keyRenamed); k.SecretKept || k.LastUsedAt != nil || k.Period() != time.Minute {
		t.Errorf("renamed key %+v", k)
	}

	// Accounts: the quota where it was fetched, unknown where it was not.
	a, err := st.Account(ctx, f.account)
	if err != nil {
		t.Fatal(err)
	}
	if a.Quota.FiveHourUtil != 42.5 || a.Quota.SevenDayUtil != -1 || a.Quota.FiveHourStatus != "allowed" ||
		a.Quota.Detail != `[{"x":1}]` || a.Quota.UpdatedAt.IsZero() || a.Quota.FiveHourReset.IsZero() ||
		!a.Quota.SevenDayReset.IsZero() {
		t.Errorf("fetched quota %+v", a.Quota)
	}
	if a.LastError != "boom" || a.RefreshDeadAt == nil || a.RefreshExpiresAt.IsZero() || len(a.ModelsOff) != 1 {
		t.Errorf("account %+v", a)
	}
	never, _ := st.Account(ctx, f.accountNever)
	if never.Quota.Known() || !never.Quota.UpdatedAt.IsZero() || never.LastError != "" || !never.Disabled() ||
		never.Position != 1 {
		t.Errorf("never-fetched account %+v", never)
	}
	if n := count(`SELECT COUNT(*) FROM account_quota`); n != 1 {
		t.Errorf("%d quota rows, want the fetched account's alone", n)
	}

	// 031: every row an account served was Anthropic's, the only provider
	// there was; a refusal no account served names none. Nothing has a plan
	// or reasoning tokens yet.
	if n := count(`SELECT COUNT(*) FROM usage_events WHERE account_id <> '' AND provider <> 'anthropic'`); n != 0 {
		t.Errorf("%d served rows not filed as anthropic", n)
	}
	if n := count(`SELECT COUNT(*) FROM usage_events WHERE account_id = '' AND provider <> ''`); n != 0 {
		t.Errorf("%d unserved rows given a provider", n)
	}
	if a.Plan != "" || count(`SELECT COUNT(*) FROM usage_events WHERE reasoning_tokens <> 0`) != 0 {
		t.Errorf("the upgrade invented a plan or reasoning: %q", a.Plan)
	}
	// And the upgraded table takes a second account at the same address with
	// another provider account id — a Team workspace beside a personal one.
	other, err := st.UpsertAccount(ctx, Account{Provider: "anthropic", Email: "one@example.com",
		AccountUUID: "u2", Plan: "team", ExpiresAt: time.Now().Add(time.Hour)},
		Tokens{AccessToken: "a", RefreshToken: "r"})
	if err != nil || other.ID == f.account || other.Plan != "team" {
		t.Errorf("second workspace at the same address = %+v, %v", other, err)
	}

	// 032: every key is a custom client in direct mode under its own name,
	// its switched-off models its direct-mode switches, with no slots; the
	// aliases and every binding of them are gone.
	all, err := st.Clients(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, c := range all {
		names[c.ID] = c.Name
		if c.Type != "custom" || c.Mode != "direct" || len(c.Slots) != 0 ||
			c.DisabledAt != nil {
			t.Errorf("client %s = %+v, want a custom one in direct mode with no slots", c.ID, c)
		}
	}
	if !reflect.DeepEqual(names, map[string]string{f.keyKept: "laptop", f.keyRenamed: "after"}) {
		t.Errorf("clients %v, want one per key", names)
	}
	if k, _ := st.Key(ctx, f.keyKept); k.ClientType != "custom" || k.NamesSlots() || k.Allows("claude-opus-4-1") {
		t.Errorf("the kept key reads %+v: not a direct-mode client with its switch", k)
	}
	if n := count(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'model_roles'`); n != 0 {
		t.Error("model_roles survived the upgrade")
	}

	// The rest.
	if v, ok, _ := st.Setting(ctx, "api.openai"); !ok || v != "off" {
		t.Errorf("setting = %q, %v", v, ok)
	}
	if _, ok, _ := st.Setting(ctx, "gateway.enabled"); ok {
		t.Error("the gateway was switched off though no API was")
	}
	if u, err := st.AdminUpdatedAt(ctx); err != nil || !u.Equal(f.created) {
		t.Errorf("admin updated %v, %v", u, err)
	}
	if err := st.SpendLoginLink(ctx, "live"); err != nil {
		t.Errorf("a live link did not survive: %v", err)
	}
	if n := count(`SELECT COUNT(*) FROM login_links`); n != 0 {
		t.Errorf("%d links left, want the expired one dropped and the live one spent", n)
	}

	// And the foreign keys the upgrade added do their job: a key's and an
	// account's rows go with them, the key's client with it.
	if err := st.DeleteKey(ctx, f.keyKept); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteAccount(ctx, f.account); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM api_key_models_off`: 0,
		`SELECT COUNT(*) FROM clients`:            1,
		`SELECT COUNT(*) FROM account_models_off`: 0,
		`SELECT COUNT(*) FROM account_quota`:      0,
		`SELECT COUNT(*) FROM usage_events`:       f.events,
	} {
		if n := count(q); n != want {
			t.Errorf("%s = %d after deleting, want %d", q, n, want)
		}
	}
}

// 032 makes one gateway switch of the two API switches, off only where both
// were, and deletes the gateway's own titling key, keeping its history.
func TestUpgradeMergesTheSwitchesAndDropsTheTitler(t *testing.T) {
	for _, c := range []struct {
		anthropic, openai string
		off               bool
	}{{"false", "false", true}, {"false", "true", false}, {"true", "false", false}} {
		p := filepath.Join(t.TempDir(), "v28.db")
		db := buildOld(t, dsn(p), 28)
		f := fillV28(t, db)
		for k, v := range map[string]string{"api.anthropic.enabled": c.anthropic, "api.openai.enabled": c.openai,
			"chat.titles.key": f.keyRenamed} {
			if _, err := db.Exec(`INSERT INTO settings VALUES (?, ?, 1760000000)`, k, v); err != nil {
				t.Fatal(err)
			}
		}
		db.Close()
		st, err := Open(context.Background(), p, testSealer(t))
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		v, ok, _ := st.Setting(ctx, "gateway.enabled")
		if c.off != (ok && v == "false") {
			t.Errorf("%s/%s: gateway.enabled = %q, %v", c.anthropic, c.openai, v, ok)
		}
		if _, ok, _ := st.Setting(ctx, "api.openai.enabled"); ok {
			t.Error("the per-API switch survived")
		}
		if _, err := st.Client(ctx, f.keyRenamed); !errors.Is(err, ErrClientNotFound) {
			t.Errorf("the titling key became a client: %v", err)
		}
		// Deleted, with its titling settings; its history stays, under the name
		// it last had.
		if _, err := st.Key(ctx, f.keyRenamed); !errors.Is(err, ErrKeyNotFound) {
			t.Errorf("the titling key survived: %v", err)
		}
		if _, ok, _ := st.Setting(ctx, "chat.titles.key"); ok {
			t.Error("the titling key's setting survived")
		}
		rep, err := st.Usage(ctx, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		named := false
		for _, b := range rep.ByKey {
			named = named || (b.ID == f.keyRenamed && b.Label == "after")
		}
		if !named {
			t.Errorf("the titling key's history lost its name: %+v", rep.ByKey)
		}

		st.Close()
	}
}

// A migrated client names its chats as the gateway-wide switch did: on only
// where it was switched on. Absent was its default, which was off.
func TestUpgradeCarriesTheTitlingSwitch(t *testing.T) {
	for setting, want := range map[string]bool{"": false, "true": true, "false": false} {
		p := filepath.Join(t.TempDir(), "v28.db")
		db := buildOld(t, dsn(p), 28)
		f := fillV28(t, db)
		if setting != "" {
			if _, err := db.Exec(`INSERT INTO settings VALUES ('chat.titles.enabled', ?, 1760000000)`, setting); err != nil {
				t.Fatal(err)
			}
		}
		db.Close()
		st, err := Open(context.Background(), p, testSealer(t))
		if err != nil {
			t.Fatal(err)
		}
		c, err := st.Client(context.Background(), f.keyKept)
		if err != nil {
			t.Fatal(err)
		}
		if c.NameChats != want {
			t.Errorf("chat.titles.enabled %q: the migrated client names chats = %v, want %v", setting, c.NameChats, want)
		}
		st.Close()
	}
}

// A migrated client fits images as the gateway-wide switch did for it: on
// only where it was switched on, absent being its default, off. The setting
// does not survive, since nothing reads it any more.
func TestUpgradeCarriesTheImageFitSwitch(t *testing.T) {
	for setting, want := range map[string]bool{"": false, "true": true, "false": false} {
		p := filepath.Join(t.TempDir(), "v28.db")
		db := buildOld(t, dsn(p), 28)
		f := fillV28(t, db)
		if setting != "" {
			if _, err := db.Exec(`INSERT INTO settings VALUES ('passthrough.fit-oversized-images', ?, 1760000000)`, setting); err != nil {
				t.Fatal(err)
			}
		}
		db.Close()
		st, err := Open(context.Background(), p, testSealer(t))
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		c, err := st.Client(ctx, f.keyKept)
		if err != nil {
			t.Fatal(err)
		}
		if c.FitImages != want {
			t.Errorf("fit-oversized-images %q: the migrated client fits images = %v, want %v", setting, c.FitImages, want)
		}
		if _, ok, _ := st.Setting(ctx, "passthrough.fit-oversized-images"); ok {
			t.Errorf("fit-oversized-images %q: the gateway-wide setting survived", setting)
		}
		st.Close()
	}
}

// Every client comes out of 039 with its agent unable to manage it, whatever
// else it had: the switch is the operator's to turn on.
func TestUpgradeLeavesAgentManagementOff(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "v28.db")
	db := buildOld(t, dsn(p), 28)
	f := fillV28(t, db)
	db.Close()
	st, err := Open(ctx, p, testSealer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	k, err := st.Key(ctx, f.keyKept)
	if err != nil {
		t.Fatal(err)
	}
	if k.AgentManage || k.ClientType == "" {
		t.Errorf("the migrated client: agent_manage %v, type %q", k.AgentManage, k.ClientType)
	}
	if err := st.SetClientAgentManage(ctx, f.keyKept, true); err != nil {
		t.Errorf("the migrated client cannot be switched: %v", err)
	}
}

// A database the upgrade cannot start from is refused, with the reason, and
// left as it was.
func TestUpgradeRefusesWhatItCannotRead(t *testing.T) {
	ctx := context.Background()

	t.Run("older than 028", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "v27.db")
		buildOld(t, dsn(p), 27).Close()
		_, err := Open(ctx, p, testSealer(t))
		if err == nil || !strings.Contains(err.Error(), "028") {
			t.Fatalf("Open = %v, want a refusal naming 028", err)
		}
		db, _ := sql.Open("sqlite", dsn(p))
		defer db.Close()
		var v int
		if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil || v != 27 {
			t.Errorf("the refused database is at %d, %v; want it untouched at 27", v, err)
		}
	})

	t.Run("newer than this release", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "next.db")
		st, err := Open(ctx, p, testSealer(t))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`INSERT INTO schema_migrations VALUES (?, 0)`, schemaVersion+1); err != nil {
			t.Fatal(err)
		}
		st.Close()
		if _, err := Open(ctx, p, testSealer(t)); err == nil || !strings.Contains(err.Error(), "newer") {
			t.Fatalf("Open = %v, want a refusal saying the database is newer", err)
		}
	})

	t.Run("tables but no version", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "other.db")
		db, _ := sql.Open("sqlite", dsn(p))
		if _, err := db.Exec(`CREATE TABLE something (x)`); err != nil {
			t.Fatal(err)
		}
		db.Close()
		if _, err := Open(ctx, p, testSealer(t)); err == nil || !strings.Contains(err.Error(), "schema_migrations") {
			t.Fatalf("Open = %v, want a refusal", err)
		}
	})
}

// 035 drops a client's API: a crush made on Responses, with the gpt-5 names
// it was given there, comes through as a crush with those names, its slots,
// switches and dates as they were; an opencode made there gains the claude
// mark its native API needs on a name that lacks it.
func TestUpgradeDropsTheClientsAPI(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "v34.db")
	db := buildOld(t, dsn(p), 28)
	f := fillV28(t, db)
	steps, err := upgradeSteps(28)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range steps {
		if s.version > 34 {
			break
		}
		if err := applyStep(ctx, db, s); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`UPDATE clients SET type = 'crush', dialect = 'openai', mode = 'binding', disabled_at = 5,
		        configured_at = 7, name_chats = 0 WHERE key_id = '` + f.keyKept + `'`,
		`INSERT INTO client_slots VALUES ('` + f.keyKept + `', 'large', 'gpt-5-large', '', 'claude:opus', 1, 2),
		                                 ('` + f.keyKept + `', 'small', 'gpt-5-small', 'gpt-6-luna', '', 3, 4)`,
		// An opencode made on its old default, Responses, with the names it
		// was given there: one without the mark, one that has it.
		`UPDATE clients SET type = 'opencode', dialect = 'openai' WHERE key_id = '` + f.keyRenamed + `'`,
		`INSERT INTO client_slots VALUES ('` + f.keyRenamed + `', 'main', 'large', '', 'claude:opus', 1, 2),
		                                 ('` + f.keyRenamed + `', 'small', 'my-claude', '', 'claude:haiku', 3, 4)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	st, err := Open(ctx, p, testSealer(t))
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer st.Close()
	c, err := st.Client(ctx, f.keyKept)
	if err != nil {
		t.Fatal(err)
	}
	if c.Type != "crush" || c.Mode != "binding" || c.DisabledAt == nil || c.ConfiguredAt == nil || c.NameChats ||
		len(c.Slots) != 2 || c.Slots[0].Name != "gpt-5-large" || c.Slots[0].Follows != "claude:opus" ||
		c.Slots[1].Follows != "openai:luna" { // 040 binds the version to its family
		t.Errorf("client after 035 = %+v", c)
	}
	// opencode on Messages needs "claude" in a name; crush's names are left.
	oc, err := st.Client(ctx, f.keyRenamed)
	if err != nil {
		t.Fatal(err)
	}
	if oc.Type != "opencode" || len(oc.Slots) != 2 || oc.Slots[0].Name != "claude-large" || oc.Slots[1].Name != "my-claude" {
		t.Errorf("opencode after 035 = %+v", oc.Slots)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('clients') WHERE name = 'dialect'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("dialect survived: %d, %v", n, err)
	}
	// The slots still go with their client.
	if err := st.DeleteKey(ctx, f.keyKept); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM client_slots WHERE key_id = ?`, f.keyKept).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d slots outlived their client, %v", n, err)
	}
}

// 040 moves a slot on ChatGPT's ranking onto the line of the model it served
// — first onto sol, light onto luna — and a slot bound to a version onto its
// family, unless its provider offers every model; an id of no family is left.
func TestUpgradeBindsFamilies(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "v39.db")
	db := buildOld(t, dsn(p), 28)
	f := fillV28(t, db)
	steps, err := upgradeSteps(28)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range steps {
		if s.version > 39 {
			break
		}
		if err := applyStep(ctx, db, s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE clients SET type = 'crush', mode = 'binding' WHERE key_id = ?`, f.keyKept); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM client_slots WHERE key_id = ?`, f.keyKept); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO client_slots VALUES (?, 'large', 'large', '', 'openai:first', 1, 2),
	                                                    (?, 'small', 'small', '', 'openai:light', 3, 4),
	                                                    (?, 'x-1', 'opus', '', 'claude:opus', 5, 6),
	                                                    (?, 'x-2', 'astra', 'gpt-6-astra', '', 7, 8),
	                                                    (?, 'x-3', 'sol', 'gpt-6.1-sol', '', 9, 10),
	                                                    (?, 'x-4', 'sonnet', 'claude-sonnet-5', '', 11, 12),
	                                                    (?, 'x-5', 'odd', 'gpt-reserve', '', 13, 14)`,
		f.keyKept, f.keyKept, f.keyKept, f.keyKept, f.keyKept, f.keyKept, f.keyKept); err != nil {
		t.Fatal(err)
	}
	// Claude offering every model: its versions stay versions.
	if _, err := db.Exec(`INSERT INTO settings VALUES ('models.anthropic.families', 'false', 1)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	st, err := Open(ctx, p, testSealer(t))
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer st.Close()
	c, err := st.Client(ctx, f.keyKept)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, sl := range c.Slots {
		got[sl.Name] = sl.Model + string(sl.Follows)
	}
	want := map[string]string{"large": "openai:sol", "small": "openai:luna", "opus": "claude:opus",
		"astra": "openai:astra", "sol": "openai:sol", "sonnet": "claude-sonnet-5", "odd": "gpt-reserve"}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s after 040 = %q, want %q", name, got[name], w)
		}
	}
}
