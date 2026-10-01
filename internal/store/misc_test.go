package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Editing a key must change its label and limits without touching the
// credential, or every client holding it would have to be reconfigured.
func TestUpdateKeyKeepsTheSecret(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	key, plaintext, err := st.CreateKey(ctx, "laptop", KeyLimits{RPMLimit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if got := key.Display(); got != KeyPrefix+key.Prefix+"…" || !strings.HasPrefix(plaintext, KeyPrefix+key.Prefix) {
		t.Errorf("Display = %q, not the prefix of %q", got, plaintext)
	}

	limits := KeyLimits{RPMLimit: 100, RatePeriod: time.Hour, TokenBudget: 5000}
	if err := st.UpdateKey(ctx, key.ID, "  desktop ", limits); err != nil {
		t.Fatalf("UpdateKey: %v", err)
	}
	got, err := st.Authenticate(ctx, plaintext)
	if err != nil {
		t.Fatalf("Authenticate after update: %v", err)
	}
	if got.Name != "desktop" || got.KeyLimits != limits {
		t.Errorf("after update: name %q limits %+v; want desktop %+v", got.Name, got.KeyLimits, limits)
	}

	for name, l := range map[string]KeyLimits{
		"negative rate":   {RPMLimit: -1},
		"negative period": {RatePeriod: -time.Second},
		"negative budget": {TokenBudget: -1},
	} {
		if err := st.UpdateKey(ctx, key.ID, "x", l); err == nil {
			t.Errorf("UpdateKey accepted %s", name)
		}
		if _, _, err := st.CreateKey(ctx, "x", l); err == nil {
			t.Errorf("CreateKey accepted %s", name)
		}
	}
	if err := st.UpdateKey(ctx, key.ID, "   ", KeyLimits{}); err == nil {
		t.Error("UpdateKey accepted a blank name")
	}
	if _, _, err := st.CreateKey(ctx, " ", KeyLimits{}); err == nil {
		t.Error("CreateKey accepted a blank name")
	}
	if err := st.UpdateKey(ctx, "ghost", "x", KeyLimits{}); !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("UpdateKey(unknown) = %v, want ErrKeyNotFound", err)
	}
}

// A zero period means a minute — what every key made before periods existed
// meant — and it is stored as such so the limiter never divides by zero.
func TestKeyPeriodDefault(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if p := (KeyLimits{}).Period(); p != time.Minute {
		t.Errorf("zero Period = %v, want a minute", p)
	}
	_, plaintext, err := st.CreateKey(ctx, "k", KeyLimits{RPMLimit: 3})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := st.Authenticate(ctx, plaintext)
	if got.RatePeriod != time.Minute {
		t.Errorf("stored period = %v, want a minute", got.RatePeriod)
	}
}

// Last use is how an operator spots a key nothing uses any more.
func TestTouchKeyRecordsLastUse(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	key, plaintext, err := st.CreateKey(ctx, "k", KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Authenticate(ctx, plaintext); got.LastUsedAt != nil {
		t.Fatalf("new key has LastUsedAt %v", got.LastUsedAt)
	}
	st.TouchKey(ctx, key.ID)
	st.TouchKey(ctx, "ghost") // best-effort: must not panic or error out

	got, _ := st.Authenticate(ctx, plaintext)
	if got.LastUsedAt == nil || time.Since(*got.LastUsedAt) > time.Minute {
		t.Errorf("Authenticate LastUsedAt = %v, want just now", got.LastUsedAt)
	}
	keys, _ := st.ListKeys(ctx)
	if len(keys) != 1 || keys[0].LastUsedAt == nil {
		t.Errorf("ListKeys does not show last use: %+v", keys)
	}
}

// First writer wins, so a chat the operator has learned to recognise is never
// renamed by a later turn; and an over-long answer is bounded.
func TestChatTitles(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if _, ok, err := st.ChatTitle(ctx, "c1"); ok || err != nil {
		t.Fatalf("ChatTitle(missing) = %v, %v", ok, err)
	}
	if err := st.SetChatTitle(ctx, "c1", "  Fixing the build  ", "haiku"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetChatTitle(ctx, "c1", "Something else", "haiku"); err != nil {
		t.Fatal(err)
	}
	if title, ok, _ := st.ChatTitle(ctx, "c1"); !ok || title != "Fixing the build" {
		t.Errorf("title = %q, %v; want the first, trimmed", title, ok)
	}

	// Nothing to name, or nothing to name it: no row, no error.
	if err := st.SetChatTitle(ctx, "", "orphan", "m"); err != nil {
		t.Error(err)
	}
	if err := st.SetChatTitle(ctx, "c2", "   ", "m"); err != nil {
		t.Error(err)
	}
	if _, ok, _ := st.ChatTitle(ctx, "c2"); ok {
		t.Error("blank title was stored")
	}

	if err := st.SetChatTitle(ctx, "c3", strings.Repeat("a", 300), "m"); err != nil {
		t.Fatal(err)
	}
	if title, _, _ := st.ChatTitle(ctx, "c3"); len(title) != maxTitle {
		t.Errorf("title length %d, want capped at %d", len(title), maxTitle)
	}
}

// Titles are pruned with the events they name, or the table grows forever
// with names for chats nobody can open.
func TestPruneChatTitlesFollowsTheEvents(t *testing.T) {
	ctx := context.Background()
	st := usageStore(t)

	record(t, st, UsageEvent{ConversationID: "live", Status: 200})
	for _, id := range []string{"live", "gone"} {
		if err := st.SetChatTitle(ctx, id, "title "+id, "m"); err != nil {
			t.Fatal(err)
		}
	}
	n, err := st.PruneChatTitles(ctx)
	if err != nil || n != 1 {
		t.Fatalf("PruneChatTitles = %d, %v; want 1", n, err)
	}
	if _, ok, _ := st.ChatTitle(ctx, "live"); !ok {
		t.Error("title of a chat with events was pruned")
	}
	if _, ok, _ := st.ChatTitle(ctx, "gone"); ok {
		t.Error("title of a chat with no events survived")
	}
}

// The chat menu is unusable as a list of hex ids, so a titled chat must carry
// its title, an untitled one its bare id, and requests with no chat must not
// appear as one.
func TestChatFacetUsesTitles(t *testing.T) {
	ctx := context.Background()
	st := usageStore(t)

	record(t, st, UsageEvent{ConversationID: "aaa", Status: 200})
	record(t, st, UsageEvent{ConversationID: "aaa", Status: 200})
	record(t, st, UsageEvent{ConversationID: "bbb", Status: 200})
	record(t, st, UsageEvent{Status: 200})
	record(t, st, UsageEvent{Status: 200})
	record(t, st, UsageEvent{Status: 200, Rejected: true})
	if err := st.SetChatTitle(ctx, "aaa", "Refactor", "m"); err != nil {
		t.Fatal(err)
	}

	// Out-of-range limits fall back to the default rather than erroring.
	f, err := st.RequestFacets(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Chats) != 2 {
		t.Fatalf("chats = %+v, want aaa and bbb only", f.Chats)
	}
	if f.Chats[0].Value != "aaa" || f.Chats[0].Label != "Refactor" || f.Chats[0].Count != 2 {
		t.Errorf("first chat = %+v, want aaa titled Refactor with 2", f.Chats[0])
	}
	if f.Chats[1].Value != "bbb" || f.Chats[1].Label != "" {
		t.Errorf("second chat = %+v, want bbb untitled", f.Chats[1])
	}
	kinds := map[string]int64{}
	for _, k := range f.Kinds {
		kinds[k.Value] = k.Count
	}
	if kinds["relayed"] != 5 || kinds["rejected"] != 1 {
		t.Errorf("kinds = %+v, want 5 relayed and 1 rejected", f.Kinds)
	}

	// The cap holds per column.
	f, err = st.RequestFacets(ctx, 1)
	if err != nil || len(f.Chats) != 1 {
		t.Errorf("limit 1: chats = %+v, %v", f.Chats, err)
	}
	if _, err := st.RequestFacets(ctx, 1000); err != nil {
		t.Error(err)
	}
}

// Both kinds ticked is no filter, and each alone narrows; this is the case the
// menu's checkboxes produce.
func TestRequestFilterKinds(t *testing.T) {
	ctx := context.Background()
	st := usageStore(t)
	now := time.Now()
	record(t, st, UsageEvent{At: now.Add(-2 * time.Second), Status: 200})
	record(t, st, UsageEvent{At: now.Add(-1 * time.Second), Status: 401, Rejected: true})

	for _, c := range []struct {
		kinds []string
		want  int
	}{
		{nil, 2},
		{[]string{"relayed"}, 1},
		{[]string{"rejected"}, 1},
		{[]string{"relayed", "rejected"}, 2},
	} {
		ev, _, err := st.RecentUsage(ctx, 10, UsageCursor{}, RequestFilter{Kinds: c.kinds})
		if err != nil || len(ev) != c.want {
			t.Errorf("kinds %v: %d rows, %v; want %d", c.kinds, len(ev), err, c.want)
		}
	}
}

// KeySpend is what a token budget is enforced against, so it must count all
// four billed columns, only this key's relayed traffic, and only the window.
func TestKeySpendAndOldestSpend(t *testing.T) {
	ctx := context.Background()
	st := usageStore(t)
	now := time.Now()
	since := now.Add(-24 * time.Hour)

	record(t, st, UsageEvent{At: now.Add(-48 * time.Hour), KeyID: "k", InputTokens: 1000, Status: 200})
	record(t, st, UsageEvent{At: now.Add(-3 * time.Hour), KeyID: "k", Status: 200})
	record(t, st, UsageEvent{At: now.Add(-2 * time.Hour), KeyID: "k", Status: 200,
		InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, CacheWriteTokens: 4})
	record(t, st, UsageEvent{At: now.Add(-1 * time.Hour), KeyID: "k", Status: 200, InputTokens: 10})
	record(t, st, UsageEvent{At: now.Add(-1 * time.Hour), KeyID: "k", Status: 401, Rejected: true, InputTokens: 500})
	record(t, st, UsageEvent{At: now.Add(-1 * time.Hour), KeyID: "other", Status: 200, InputTokens: 700})

	spend, err := st.KeySpend(ctx, "k", since)
	if err != nil || spend != 20 {
		t.Errorf("KeySpend = %d, %v; want 20", spend, err)
	}
	if e := (UsageEvent{InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, CacheWriteTokens: 4}); e.Tokens() != 10 {
		t.Errorf("Tokens() = %d, want 10", e.Tokens())
	}

	// The zero-token request three hours ago freed nothing, so the budget
	// starts freeing up at the first one that spent.
	oldest, ok := st.OldestSpendAt(ctx, "k", since)
	if !ok || !oldest.Equal(now.Add(-2*time.Hour).UTC()) {
		t.Errorf("OldestSpendAt = %v, %v; want %v", oldest, ok, now.Add(-2*time.Hour).UTC())
	}
	if _, ok := st.OldestSpendAt(ctx, "idle", since); ok {
		t.Error("OldestSpendAt reported spend for a key with none")
	}
	if n, _ := st.KeySpend(ctx, "idle", since); n != 0 {
		t.Errorf("idle key spend = %d", n)
	}
}

// The keys and accounts lists show per-row totals; a key doing nothing must be
// visibly absent, and refused requests must not be charged to anyone.
func TestKeyAndAccountUsage(t *testing.T) {
	ctx := context.Background()
	st := usageStore(t)
	now := time.Now()

	record(t, st, UsageEvent{At: now.Add(-time.Minute), KeyID: "k1", AccountID: "a1", Status: 200,
		InputTokens: 10, OutputTokens: 5, CacheReadTokens: 2, CacheWriteTokens: 1})
	record(t, st, UsageEvent{At: now.Add(-time.Minute), KeyID: "k1", AccountID: "a2", Status: 500})
	record(t, st, UsageEvent{At: now.Add(-time.Minute), KeyID: "k2", AccountID: "a1", Status: 200,
		Error: "overloaded_error", InputTokens: 7})
	record(t, st, UsageEvent{At: now.Add(-time.Minute), KeyID: "k3", Status: 401, Rejected: true})
	record(t, st, UsageEvent{At: now.Add(-48 * time.Hour), KeyID: "k4", AccountID: "a3", Status: 200})

	keys, err := st.KeyUsage(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Errorf("KeyUsage has %d keys, want k1 and k2", len(keys))
	}
	k1 := keys["k1"]
	if k1.Requests != 2 || k1.Errors != 1 || k1.InputTokens != 10 || k1.OutputTokens != 5 || k1.CacheTokens != 3 {
		t.Errorf("k1 = %+v", k1)
	}
	if keys["k2"].Errors != 1 {
		t.Errorf("k2 = %+v, want the failed stream counted as an error", keys["k2"])
	}

	accts, err := st.AccountUsage(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(accts) != 2 || accts["a1"].Requests != 2 || accts["a1"].InputTokens != 17 || accts["a2"].Errors != 1 {
		t.Errorf("AccountUsage = %+v", accts)
	}

	// An empty window is an empty map, not nil-with-error.
	empty, err := st.KeyUsage(ctx, now.Add(time.Hour))
	if err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("empty window = %v, %v", empty, err)
	}
	if tot, err := st.Totals(ctx, now.Add(time.Hour)); err != nil || tot.Requests != 0 || tot.MedianMS != 0 {
		t.Errorf("Totals on an empty window = %+v, %v", tot, err)
	}
}

// Reopening is what every restart does: migrations must not reapply, data must
// survive, and the file must stay private because it holds sealed credentials.
func TestReopenIsIdempotentAndPrivate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "re.db")
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, "k", "v"); err != nil {
		t.Fatal(err)
	}
	var migrations int
	st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&migrations)
	st.Close()

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st.Close()
	if v, ok, _ := st.Setting(ctx, "k"); !ok || v != "v" {
		t.Errorf("setting after reopen = %q, %v", v, ok)
	}
	var again int
	st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&again)
	if again != migrations || migrations == 0 {
		t.Errorf("migrations %d then %d, want the same non-zero count", migrations, again)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("database mode %o, want 600", perm)
	}
	if mode, err := st.AutoVacuum(ctx); err != nil || mode != 2 {
		t.Errorf("AutoVacuum = %d, %v; want incremental (2)", mode, err)
	}
}

// A database that cannot be opened must fail at startup, not on the first
// request.
func TestOpenFailsOnAnUnusablePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "dir", "x.db")
	if st, err := Open(context.Background(), path); err == nil {
		st.Close()
		t.Error("Open succeeded in a directory that does not exist")
	}
}

// Once the database is gone every call must report it, and the best-effort
// writers on the request path must neither panic nor block.
func TestClosedStoreReportsErrors(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	sealer := testSealer(t)
	st.Close()

	st.TouchKey(ctx, "k")
	st.MarkAccountError(ctx, "a", "x")
	st.MarkAccountUsed(ctx, "a")
	if _, ok := st.OldestSpendAt(ctx, "k", time.Time{}); ok {
		t.Error("OldestSpendAt reported spend from a closed store")
	}

	since := time.Now().Add(-time.Hour)
	checks := map[string]error{}
	_, checks["UpsertAccount"] = st.UpsertAccount(ctx, sealer, Account{Provider: "p", Email: "e"}, Tokens{})
	_, checks["ListAccounts"] = st.ListAccounts(ctx)
	_, checks["Account"] = st.Account(ctx, "a")
	_, checks["AccountByEmail"] = st.AccountByEmail(ctx, "p", "e")
	checks["SetAccountOrder"] = st.SetAccountOrder(ctx, []string{"a"})
	checks["SetAccountQuota"] = st.SetAccountQuota(ctx, "a", AccountQuota{})
	_, checks["AccountTokens"] = st.AccountTokens(ctx, sealer, "a")
	checks["UpdateAccountTokens"] = st.UpdateAccountTokens(ctx, sealer, "a", Tokens{}, time.Now(), time.Time{})
	checks["MarkRefreshDead"] = st.MarkRefreshDead(ctx, "a", "x")
	checks["SetAccountDisabled"] = st.SetAccountDisabled(ctx, "a", true)
	checks["SetAccountEnabled"] = st.SetAccountDisabled(ctx, "a", false)
	checks["DeleteAccount"] = st.DeleteAccount(ctx, "a")
	_, checks["AdminExists"] = st.AdminExists(ctx)
	checks["CreateAdmin"] = st.CreateAdmin(ctx, "long enough")
	_, checks["VerifyAdmin"] = st.VerifyAdmin(ctx, "long enough")
	checks["ChangeAdminPassword"] = st.ChangeAdminPassword(ctx, "long enough", "long enough")
	checks["DeleteAdmin"] = st.DeleteAdmin(ctx)
	_, checks["AdminUpdatedAt"] = st.AdminUpdatedAt(ctx)
	_, _, checks["MintLoginLink"] = st.MintLoginLink(ctx, 0)
	checks["SpendLoginLink"] = st.SpendLoginLink(ctx, "t")
	_, _, checks["CreateKey"] = st.CreateKey(ctx, "k", KeyLimits{})
	_, checks["Authenticate"] = st.Authenticate(ctx, KeyPrefix+strings.Repeat("0", 48))
	_, checks["ListKeys"] = st.ListKeys(ctx)
	checks["UpdateKey"] = st.UpdateKey(ctx, "k", "n", KeyLimits{})
	checks["DeleteKey"] = st.DeleteKey(ctx, "k")
	_, _, checks["ChatTitle"] = st.ChatTitle(ctx, "c")
	checks["SetChatTitle"] = st.SetChatTitle(ctx, "c", "t", "m")
	_, checks["PruneChatTitles"] = st.PruneChatTitles(ctx)
	_, checks["Chats"] = st.Chats(ctx, since, 10)
	_, checks["ChatEvents"] = st.ChatEvents(ctx, "c", 10)
	_, _, checks["Setting"] = st.Setting(ctx, "k")
	_, checks["Settings"] = st.Settings(ctx)
	checks["SetSetting"] = st.SetSetting(ctx, "k", "v")
	checks["RecordUsage"] = st.RecordUsage(ctx, UsageEvent{At: time.Now()})
	_, checks["Totals"] = st.Totals(ctx, since)
	_, checks["Usage"] = st.Usage(ctx, since)
	_, checks["RequestFacets"] = st.RequestFacets(ctx, 10)
	_, _, checks["RecentUsage"] = st.RecentUsage(ctx, 10, UsageCursor{}, RequestFilter{})
	_, checks["KeyUsage"] = st.KeyUsage(ctx, since)
	_, checks["KeySpend"] = st.KeySpend(ctx, "k", since)
	_, checks["AccountUsage"] = st.AccountUsage(ctx, since)
	_, checks["PruneUsage"] = st.PruneUsage(ctx, time.Hour)
	checks["Snapshot"] = st.Snapshot(ctx, filepath.Join(t.TempDir(), "snap.db"))
	_, _, checks["Vacuum"] = st.Vacuum(ctx)
	_, checks["AutoVacuum"] = st.AutoVacuum(ctx)

	for name, err := range checks {
		if err == nil {
			t.Errorf("%s on a closed store returned no error", name)
		}
	}
}
