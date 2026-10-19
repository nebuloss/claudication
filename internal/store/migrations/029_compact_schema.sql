-- Version 28 to 29: the whole schema rebuilt as schema.sql has it.
--
-- What changes, and why each is in schema.sql beside the table it concerns:
-- every moment becomes INTEGER unix ms; usage_events trades `rejected` and the
-- '/internal/title' path test for `kind`, and key_name/account_email for the
-- names table; the quota moves to account_quota, with absence and NULL for the
-- old -1 and ''; model_roles' gateway rows get key_id NULL and a foreign key;
-- rpm_limit, virtual_model and usage_json become rate_limit, alias and detail;
-- AUTOINCREMENT and the indexes nothing needed go.
--
-- Built by setting every old table aside under legacy_*, creating the new ones
-- from schema.sql as it stood at version 29 (copied in below), copying across,
-- and dropping the legacy tables. This step used to include schema.sql itself,
-- so an upgraded database and a fresh one could not differ by a typo; that was
-- only right while schema.sql was at version 29, and step 030 froze it into
-- the copy below. Do not edit the copy: it is what version 29 was, and every
-- later step starts from it.
--
-- Runs inside migrate()'s transaction with foreign_keys=ON, which cannot be
-- turned off inside one. That decides the order: renaming a parent re-points
-- its children's REFERENCES at the new name, so the legacy children follow
-- their legacy parents; the copy fills parents before children; and the legacy
-- children are dropped before their parents, so no DROP cascades into a row
-- still wanted.

------------------------------------------------------------------ set aside
-- Index names are global, and idx_usage_at is reused. sqlite_autoindex_*
-- follow their table's rename by themselves.
DROP INDEX IF EXISTS idx_api_keys_prefix;
DROP INDEX IF EXISTS idx_accounts_provider;
DROP INDEX IF EXISTS idx_accounts_position;
DROP INDEX IF EXISTS idx_login_links_expires;
DROP INDEX IF EXISTS idx_usage_at;
DROP INDEX IF EXISTS idx_usage_chat_at;
DROP INDEX IF EXISTS idx_usage_conversation_at;
DROP INDEX IF EXISTS idx_usage_error_code_at;
DROP INDEX IF EXISTS idx_usage_rejected_at;

ALTER TABLE schema_migrations  RENAME TO legacy_schema_migrations;
ALTER TABLE api_keys           RENAME TO legacy_api_keys;
ALTER TABLE api_key_models_off RENAME TO legacy_api_key_models_off;
ALTER TABLE model_roles        RENAME TO legacy_model_roles;
ALTER TABLE accounts           RENAME TO legacy_accounts;
ALTER TABLE account_models_off RENAME TO legacy_account_models_off;
ALTER TABLE usage_events       RENAME TO legacy_usage_events;
ALTER TABLE chat_titles        RENAME TO legacy_chat_titles;
ALTER TABLE admin              RENAME TO legacy_admin;
ALTER TABLE settings           RENAME TO legacy_settings;
ALTER TABLE login_links        RENAME TO legacy_login_links;

------------------------------------------------------------------ schema.sql at version 29
-- Verbatim, frozen when step 030 changed schema.sql.

-- claudication's schema, whole: what a fresh database is created with, and
-- what migrations/ brings an older one to. Version 29 (schemaVersion in
-- store.go).
--
-- Written as if from scratch. Migrations 001-028 grew it a column at a time;
-- their reasoning is kept here beside what it explains, and the files are gone
-- (see migrate). TestUpgradeMatchesFreshSchema holds this file and the upgrade
-- path to the same result.
--
-- Every moment is INTEGER unix milliseconds, UTC. There used to be three kinds
-- -- RFC3339 at whole seconds, fixed-width nine-digit text, unix seconds -- and
-- text only compares as time at a fixed width, which once let a window that
-- started on a whole second drop the events inside that second (020). Text
-- times also took 30 bytes in every index that led with them: usage_events had
-- five such indexes, and the indexes were bigger than the table.
--
-- No AUTOINCREMENT anywhere. Pruning deletes the oldest rows, so the highest
-- rowid survives and new ids keep rising without it, and the sqlite_sequence
-- write it adds to every insert bought nothing.

-- One row: the version this file is at, and when the database reached it.
-- version is the highest step applied; see migrate.
CREATE TABLE schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);

------------------------------------------------------------------ api_keys
-- Client credentials. Policy lives here rather than in the config file so it
-- changes without a restart. There is no revocation: it was one-way, so it was
-- a delete that left a row behind; deleting is the one way to withdraw a key.
CREATE TABLE api_keys (
    id            TEXT    PRIMARY KEY,
    name          TEXT    NOT NULL,
    -- The first characters of the random part, in the clear, to find the row.
    -- Not a secret: the prefix alone proves nothing.
    prefix        TEXT    NOT NULL UNIQUE,
    -- sha256 of the key, which is what authenticates: a constant-time compare
    -- that needs no sealing key on the request path.
    hash          TEXT    NOT NULL,
    -- The secret, sealed like the account tokens (internal/secret), so a key's
    -- client configs can be shown again. The usual rule -- keep only the hash
    -- -- bought nothing here: the same file holds every account's OAuth
    -- tokens, worth more than any key, under the same seal, so a stolen file
    -- is enough for neither and the state directory is enough for both.
    -- NULL only for the gateway's own titling key, whose secret is thrown away
    -- by design; ReissueUnkeptKeys gives any other NULL a new secret.
    sealed_secret BLOB,
    created_at    INTEGER NOT NULL,
    last_used_at  INTEGER,
    -- Requests per rate_period_s; 0 is the gateway's default. A period rather
    -- than a fixed minute because "two hundred an hour" is not "three a
    -- minute": the second refuses a burst of ten the first permits.
    rate_limit    INTEGER NOT NULL DEFAULT 0  CHECK (rate_limit >= 0),
    rate_period_s INTEGER NOT NULL DEFAULT 60 CHECK (rate_period_s > 0),
    -- Tokens per rolling day; 0 is unlimited.
    token_budget  INTEGER NOT NULL DEFAULT 0  CHECK (token_budget >= 0)
);

-- The models a key may not ask for. Kept as what is off, so a model released
-- tomorrow is usable by every key until someone decides otherwise, and a key
-- handed to a script can be kept off the expensive model without listing every
-- cheap one. model is as the operator switched it; requests are matched by
-- store.ModelKey, so a dated id and its alias are one model.
CREATE TABLE api_key_models_off (
    key_id TEXT    NOT NULL REFERENCES api_keys (id) ON DELETE CASCADE,
    model  TEXT    NOT NULL,
    off_at INTEGER NOT NULL,
    PRIMARY KEY (key_id, model)
) WITHOUT ROWID;

-- Model aliases: names the gateway's client configs ask for instead of real
-- models, each bound to one real model, so changing which model a client uses
-- is an edit here rather than a round of every client's configuration. Every
-- client has a slot for the model doing the work and one for titles and
-- summaries (measured 2026-10-15: crush large/small, opencode model and
-- small_model, Codex model, Claude Code's opus and haiku tiers), hence the two
-- roles claude-large and claude-small; family aliases and custom ones sit
-- beside them. See roles.go.
--
-- key_id NULL is the gateway-wide binding; a key's row overrides it for that
-- key, and no row means the key follows the gateway. NULL rather than '' so a
-- key's rows go with their key by foreign key -- DeleteKey used to remove them
-- with a second statement. An empty model on a built-in alias's gateway row
-- follows its family's newest model.
CREATE TABLE model_roles (
    key_id     TEXT    REFERENCES api_keys (id) ON DELETE CASCADE,
    role       TEXT    NOT NULL,
    model      TEXT    NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL
);
-- One binding per role gateway-wide, one per role per key. Two partial
-- indexes, because NULLs are distinct in a plain UNIQUE (key_id, role).
CREATE UNIQUE INDEX model_roles_global ON model_roles (role) WHERE key_id IS NULL;
CREATE UNIQUE INDEX model_roles_key    ON model_roles (key_id, role) WHERE key_id IS NOT NULL;

------------------------------------------------------------------ accounts
-- Upstream accounts, one row per logged-in provider account. Tokens are sealed
-- before they reach this table, so they are BLOBs, never readable text.
CREATE TABLE accounts (
    id                 TEXT    PRIMARY KEY,
    provider           TEXT    NOT NULL,
    email              TEXT    NOT NULL,
    account_uuid       TEXT    NOT NULL DEFAULT '',
    access_token       BLOB    NOT NULL,
    refresh_token      BLOB    NOT NULL,
    expires_at         INTEGER NOT NULL,
    -- When re-authorisation becomes unavoidable: the refresh token has its own
    -- lifetime, which refreshing cannot extend. Claude Code reads the same
    -- value for "Your login expires in N days" and warns three days out. NULL
    -- is the provider not saying, which is not the same as expired.
    refresh_expires_at INTEGER,
    created_at         INTEGER NOT NULL,
    last_refresh_at    INTEGER,
    last_used_at       INTEGER,
    last_error         TEXT    NOT NULL DEFAULT '',
    disabled_at        INTEGER,
    -- When the refresh token was refused as invalid_grant: revoked, expired or
    -- spent, and nothing about it improves with time. The durable version of
    -- the set Claude Code keeps of such tokens. Not a wipe of the credentials,
    -- as the client does: a gateway has to say which of several accounts needs
    -- reconnecting, and the access token may have free hours left. Cleared by
    -- a successful consent flow.
    refresh_dead_at    INTEGER,
    -- The operator's priority order, lowest first. Which account serves is a
    -- decision, not an inference: a personal subscription and a work one are
    -- not interchangeable. Quota is a reason to skip, not the ordering.
    position           INTEGER NOT NULL DEFAULT 0,
    -- Identity is set once, at login, and never rewritten by a refresh.
    -- auth2api derives the email from every refresh response, so a refresh
    -- that omits the account object renames the row to "unknown", orphaning
    -- its stats and spawning a phantom duplicate on restart.
    UNIQUE (provider, email)
);

-- What /api/oauth/usage last said about an account: the figures `/usage`
-- prints in the client. Polled rather than read off relayed responses' rate
-- limit headers, which showed nothing until an account had served a request
-- and carried only the two headline windows.
--
-- Its own table because the poller is its only writer and it changes every
-- poll, while the row beside it holds the credentials. No row is never
-- fetched, which is not zero: an account not yet heard from is unknown, not
-- idle, and the two route differently. A NULL util is a window the endpoint
-- did not report.
CREATE TABLE account_quota (
    account_id       TEXT    PRIMARY KEY REFERENCES accounts (id) ON DELETE CASCADE,
    updated_at       INTEGER NOT NULL,
    -- Percentages, 0-100.
    five_hour_util   REAL,
    five_hour_reset  INTEGER,
    five_hour_status TEXT    NOT NULL DEFAULT '',
    seven_day_util   REAL,
    seven_day_reset  INTEGER,
    seven_day_status TEXT    NOT NULL DEFAULT '',
    -- The normalised `limits` array verbatim, per-model weekly rows included,
    -- for the UI to render exactly what `/usage` prints.
    detail           TEXT    NOT NULL DEFAULT ''
);

-- The models an operator turned off for an account, by the rule
-- api_key_models_off follows: a row is an off, no row is on.
CREATE TABLE account_models_off (
    account_id TEXT    NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    model      TEXT    NOT NULL,
    off_at     INTEGER NOT NULL,
    PRIMARY KEY (account_id, model)
) WITHOUT ROWID;

------------------------------------------------------------------ names
-- What a key or an account was last called, outliving it. History must
-- survive a key or an account being deleted, so usage_events holds their ids
-- loosely, with no foreign key -- a cascade would erase exactly the record
-- looked for afterwards. It used to repeat key_name and account_email on every
-- row for the same reason, and every reader wanted the latest name anyway,
-- found with a correlated ORDER BY at DESC LIMIT 1 per id. So a rename shows
-- across all of history.
--
-- Written when a key or an account is created or renamed, never on the request
-- path. Pruned with the chat titles once nothing refers to an id.
CREATE TABLE names (
    kind TEXT NOT NULL CHECK (kind IN ('key', 'account')),
    id   TEXT NOT NULL,
    name TEXT NOT NULL,
    PRIMARY KEY (kind, id)
) WITHOUT ROWID;

------------------------------------------------------------------ usage_events
-- One row per request: an event table rather than counters, because counters
-- cannot be broken down after the fact, and a row per request is small enough
-- that retention, not size, is what to manage.
--
-- Requests the gateway refused are rows here too. A refused request is a
-- relayed one with its key, model and tokens missing, and the read that matters
-- -- one time-ordered, keyset-paged, filtered stream -- is what two tables would
-- break.
CREATE TABLE usage_events (
    id                 INTEGER PRIMARY KEY,
    -- When the request started.
    at                 INTEGER NOT NULL,
    -- 0 relayed for a client; 1 refused before the upstream, which spent
    -- nothing and has no key, model or account to group under; 2 made by the
    -- gateway for itself (store.InternalPath: naming a chat), part of a chat's
    -- cost but no evidence of who was having it. Aggregates take kind <> 1,
    -- the chat rollups kind = 0. Zero is the common case, so a caller that
    -- does not think about it files a relayed request.
    kind               INTEGER NOT NULL DEFAULT 0 CHECK (kind IN (0, 1, 2)),
    key_id             TEXT    NOT NULL DEFAULT '',
    account_id         TEXT    NOT NULL DEFAULT '',
    -- The real model that served it: what was spent, and what reports group
    -- by. alias is what the client asked for when that was an alias.
    model              TEXT    NOT NULL DEFAULT '',
    alias              TEXT    NOT NULL DEFAULT '',
    path               TEXT    NOT NULL,
    -- The chat, as the client's own headers name it -- never derived from the
    -- messages, so the gateway groups requests without looking at content.
    -- Measured on the wire 2026-09-17: Claude Code sends
    -- X-Claude-Code-Session-Id, opencode and crush x-session-id, Codex
    -- session_id; internal/api/conversation.go has the table. Empty when the
    -- client sent nothing, never a guess: a fabricated grouping is
    -- indistinguishable from a true one once stored, and the empty bucket is
    -- how an operator finds a client that needs configuring.
    conversation_id    TEXT    NOT NULL DEFAULT '',
    -- The product token from the User-Agent. Stored because it cannot be
    -- recovered: the User-Agent is not kept, and x-session-id alone cannot
    -- tell opencode from crush.
    client             TEXT    NOT NULL DEFAULT '',
    -- Which machine. For a refused request the only identity there is; for a
    -- relayed one, where it ran. Only as true as trusted-proxies makes it.
    ip                 TEXT    NOT NULL DEFAULT '',
    -- 0 is no answer at all.
    status             INTEGER NOT NULL,
    streaming          INTEGER NOT NULL DEFAULT 0,
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    duration_ms        INTEGER NOT NULL DEFAULT 0,
    -- To the first event carrying content, stalled-and-retried attempts
    -- included; 0 is not measured. Duration alone cannot tell a long answer
    -- from an upstream that accepted the request and sat on it, which is what
    -- made crush report models "not responding" on 2026-10-01.
    first_token_ms     INTEGER NOT NULL DEFAULT 0,
    -- The failure in one word, decided once when recorded: the upstream's own
    -- type, content_check, no_answer, other. Stored because the text ends in a
    -- request_id, so no two are equal and it can be neither a filter nor a
    -- menu. Empty exactly when the request worked: RecordUsage fills it for
    -- every failure, so `error_code <> ''` is what an error is.
    error_code         TEXT    NOT NULL DEFAULT '',
    -- Empty on success. A stream that failed after its 200 lands here, which
    -- is the only way to tell it from one that worked.
    error              TEXT    NOT NULL DEFAULT ''
);
-- Ascending, not DESC. The rowid rides at the end of every index entry,
-- ascending, so a backwards walk of (at) yields (at DESC, id DESC): the
-- request log's ORDER BY, with no sort. (at DESC) yielded (at DESC, id ASC)
-- and needed a temp B-tree for the last term. Serves every window: reports,
-- budgets, the prune, the unfiltered log.
CREATE INDEX idx_usage_at           ON usage_events (at);
-- A chat's rows: the chat detail, the rollup's per-chat subqueries,
-- ChatContexts, the unattributed bucket (conversation_id = ''). Deliberately
-- not partial: the planner cannot prove a bound ? is non-empty, so a partial
-- index could not serve `conversation_id = ?`, and forgetting the guard once
-- cost 2.6 s per chat list. Full measured 12.1 MB against 10.9 MB partial at
-- 300k rows.
CREATE INDEX idx_usage_conversation ON usage_events (conversation_id, at);
-- The Message filter, and its menu as a covering scan.
CREATE INDEX idx_usage_error_code   ON usage_events (error_code, at);
-- The refused requests, for Forwarded = no: a few per cent of rows, or a flood,
-- and either way only read newest first.
CREATE INDEX idx_usage_rejected     ON usage_events (at) WHERE kind = 1;
-- No index on key_id or account_id. Every statement that touches them groups
-- over a window rather than ranging on one id; 006's (key_id, at) was used
-- only as a full scan under a temp B-tree, and cost ~22 MB per 300k events
-- plus a B-tree insert on every request (dropped by 011).

------------------------------------------------------------------ the rest
-- A name for a chat. Its own table because a title belongs to the
-- conversation, not to any one request in an append-only log. The gateway asks
-- a model for it with the caller's own request plus one message, on the
-- account that served the chat, because that reads the conversation's prompt
-- cache in full (measured 2026-09-17: cache_read=11406, where a title-only
-- system prompt wrote a fresh 7886-token entry instead).
CREATE TABLE chat_titles (
    conversation_id TEXT    PRIMARY KEY,
    title           TEXT    NOT NULL,
    -- Which model wrote it, and when: a title is generated text, and an odd one
    -- deserves to say what produced it.
    model           TEXT    NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    -- What it was written from: the chat's request count and the user's typed
    -- messages at the time. A title taken from the opening turn names how a
    -- chat started, which on production (2026-10-13) was routinely wrong: all
    -- twelve crush chats titled "Generate AGENTS.md" that week opened with
    -- crush's init prompt and ran 75 to 757 requests. A title written before
    -- the second message is replaced once. 0 when the gateway did not write it.
    requests        INTEGER NOT NULL DEFAULT 0,
    turns           INTEGER NOT NULL DEFAULT 0,
    -- An operator typed it, which holds it against every automatic rename.
    manual          INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;

-- The admin account. Exactly one, and the CHECK says so: this is an
-- operator's own gateway, and a second row would be a bug.
CREATE TABLE admin (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    password_hash TEXT    NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

-- Settings an operator changes while the gateway runs. config.yaml is read
-- once and never written back -- a process that rewrites its own config turns
-- a reviewable artefact into mutable state -- which is right for listen
-- addresses and wrong for turning an API surface off at three in the morning.
-- Key/value, because the next setting should not need a migration.
CREATE TABLE settings (
    key        TEXT    PRIMARY KEY,
    value      TEXT    NOT NULL,
    updated_at INTEGER NOT NULL
) WITHOUT ROWID;

-- Single-use sign-in links: a short random token rather than an encoded
-- credential, spent on first use. Stored rather than held in memory so one can
-- be minted from a shell, which is the case that matters -- a link made from a
-- live session is no help to someone who has none.
CREATE TABLE login_links (
    token      TEXT    PRIMARY KEY,
    expires_at INTEGER NOT NULL
) WITHOUT ROWID;

------------------------------------------------------------------ end of schema.sql at version 29

------------------------------------------------------------------ copy
-- An RFC3339 text time, with or without a fraction, always ending in Z (every
-- value was written in UTC), to unix ms, written out inline because SQL has
-- no functions of its own here:
--
--   CAST(strftime('%s', substr(x,1,19)) AS INTEGER) * 1000
--   + CASE WHEN substr(x,20,1) = '.'
--          THEN CAST(substr(rtrim(substr(x,21),'Z') || '000', 1, 3) AS INTEGER)
--          ELSE 0 END
--
-- Truncated to the millisecond, as Go's UnixMilli truncates, so an old
-- request-log cursor lands on the row it named. '' and NULL become NULL, which
-- the NOT NULL columns COALESCE to 0. The columns written with
-- time.RFC3339 never had a fraction and take the first line alone.

INSERT INTO api_keys (id, name, prefix, hash, sealed_secret, created_at, last_used_at,
                      rate_limit, rate_period_s, token_budget)
SELECT id, name, prefix, hash, sealed_secret,
       COALESCE(CAST(strftime('%s', substr(created_at,1,19)) AS INTEGER) * 1000, 0),
       CAST(strftime('%s', substr(NULLIF(last_used_at,''),1,19)) AS INTEGER) * 1000,
       MAX(rpm_limit, 0),
       CASE WHEN rate_period_s > 0 THEN rate_period_s ELSE 60 END,
       MAX(token_budget, 0)
  FROM legacy_api_keys;

INSERT INTO api_key_models_off (key_id, model, off_at)
SELECT key_id, model, COALESCE(CAST(strftime('%s', substr(off_at,1,19)) AS INTEGER) * 1000, 0)
  FROM legacy_api_key_models_off;

-- The gateway's rows were key_id ''. A key's rows outlived it when DeleteKey
-- failed between its two statements; those are dropped, as the foreign key
-- would refuse them.
INSERT INTO model_roles (key_id, role, model, updated_at)
SELECT NULLIF(key_id, ''), role, model,
       COALESCE(CAST(strftime('%s', substr(updated_at,1,19)) AS INTEGER) * 1000
       + CASE WHEN substr(updated_at,20,1) = '.'
              THEN CAST(substr(rtrim(substr(updated_at,21),'Z') || '000', 1, 3) AS INTEGER) ELSE 0 END, 0)
  FROM legacy_model_roles
 WHERE key_id = '' OR key_id IN (SELECT id FROM legacy_api_keys);

INSERT INTO accounts (id, provider, email, account_uuid, access_token, refresh_token,
                      expires_at, refresh_expires_at, created_at, last_refresh_at, last_used_at,
                      last_error, disabled_at, refresh_dead_at, position)
SELECT id, provider, email, account_uuid, access_token, refresh_token,
       COALESCE(CAST(strftime('%s', substr(expires_at,1,19)) AS INTEGER) * 1000, 0),
       CAST(strftime('%s', substr(NULLIF(refresh_expires_at,''),1,19)) AS INTEGER) * 1000,
       COALESCE(CAST(strftime('%s', substr(created_at,1,19)) AS INTEGER) * 1000, 0),
       CAST(strftime('%s', substr(NULLIF(last_refresh_at,''),1,19)) AS INTEGER) * 1000,
       CAST(strftime('%s', substr(NULLIF(last_used_at,''),1,19)) AS INTEGER) * 1000,
       COALESCE(last_error, ''),
       CAST(strftime('%s', substr(NULLIF(disabled_at,''),1,19)) AS INTEGER) * 1000,
       CAST(strftime('%s', substr(NULLIF(refresh_dead_at,''),1,19)) AS INTEGER) * 1000,
       position
  FROM legacy_accounts;

-- quota_updated_at '' was never fetched: no row. -1 was a window not reported:
-- NULL.
INSERT INTO account_quota (account_id, updated_at,
                           five_hour_util, five_hour_reset, five_hour_status,
                           seven_day_util, seven_day_reset, seven_day_status, detail)
SELECT id,
       COALESCE(CAST(strftime('%s', substr(quota_updated_at,1,19)) AS INTEGER) * 1000, 0),
       CASE WHEN quota_5h_util < 0 THEN NULL ELSE quota_5h_util END,
       CAST(strftime('%s', substr(NULLIF(quota_5h_reset,''),1,19)) AS INTEGER) * 1000,
       quota_5h_status,
       CASE WHEN quota_7d_util < 0 THEN NULL ELSE quota_7d_util END,
       CAST(strftime('%s', substr(NULLIF(quota_7d_reset,''),1,19)) AS INTEGER) * 1000,
       quota_7d_status,
       usage_json
  FROM legacy_accounts
 WHERE quota_updated_at <> '';

INSERT INTO account_models_off (account_id, model, off_at)
SELECT account_id, model, COALESCE(CAST(strftime('%s', substr(off_at,1,19)) AS INTEGER) * 1000, 0)
  FROM legacy_account_models_off;

-- The names of what exists, then, for ids already deleted, the name on their
-- newest event. SQLite takes a bare column from the row MAX() picked.
INSERT INTO names (kind, id, name) SELECT 'key', id, name FROM legacy_api_keys;
INSERT INTO names (kind, id, name) SELECT 'account', id, email FROM legacy_accounts;
INSERT OR IGNORE INTO names (kind, id, name)
SELECT 'key', key_id, key_name FROM
  (SELECT key_id, key_name, MAX(id) FROM legacy_usage_events
    WHERE key_id <> '' AND key_name <> '' GROUP BY key_id);
INSERT OR IGNORE INTO names (kind, id, name)
SELECT 'account', account_id, account_email FROM
  (SELECT account_id, account_email, MAX(id) FROM legacy_usage_events
    WHERE account_id <> '' AND account_email <> '' GROUP BY account_id);

-- error_code gains the failures it did not name: a status of 0 or >= 400 with
-- no text, which the classifier, reading only text, left empty.
INSERT INTO usage_events (id, at, kind, key_id, account_id, model, alias, path,
                          conversation_id, client, ip, status, streaming,
                          input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
                          duration_ms, first_token_ms, error_code, error)
SELECT id,
       COALESCE(CAST(strftime('%s', substr(at,1,19)) AS INTEGER) * 1000
       + CASE WHEN substr(at,20,1) = '.'
              THEN CAST(substr(rtrim(substr(at,21),'Z') || '000', 1, 3) AS INTEGER) ELSE 0 END, 0),
       CASE WHEN rejected <> 0 THEN 1 WHEN path = '/internal/title' THEN 2 ELSE 0 END,
       key_id, account_id, model, virtual_model, path, conversation_id, client, ip,
       status, streaming, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
       duration_ms, first_token_ms,
       CASE WHEN error_code <> '' THEN error_code
            WHEN status = 0 AND error = '' THEN 'no_answer'
            WHEN status = 0 OR status >= 400 OR error <> '' THEN 'other'
            ELSE '' END,
       error
  FROM legacy_usage_events
 ORDER BY id;

INSERT INTO chat_titles (conversation_id, title, model, created_at, requests, turns, manual)
SELECT conversation_id, title, model,
       COALESCE(CAST(strftime('%s', substr(created_at,1,19)) AS INTEGER) * 1000
       + CASE WHEN substr(created_at,20,1) = '.'
              THEN CAST(substr(rtrim(substr(created_at,21),'Z') || '000', 1, 3) AS INTEGER) ELSE 0 END, 0),
       requests, turns, manual
  FROM legacy_chat_titles;

INSERT INTO admin (id, password_hash, created_at, updated_at)
SELECT id, password_hash,
       COALESCE(CAST(strftime('%s', substr(created_at,1,19)) AS INTEGER) * 1000, 0),
       COALESCE(CAST(strftime('%s', substr(updated_at,1,19)) AS INTEGER) * 1000, 0)
  FROM legacy_admin;

-- Was unix seconds.
INSERT INTO settings (key, value, updated_at)
SELECT key, value, updated_at * 1000 FROM legacy_settings;

-- Only the links still worth spending.
INSERT INTO login_links (token, expires_at)
SELECT token,
       CAST(strftime('%s', substr(expires_at,1,19)) AS INTEGER) * 1000
       + CASE WHEN substr(expires_at,20,1) = '.'
              THEN CAST(substr(rtrim(substr(expires_at,21),'Z') || '000', 1, 3) AS INTEGER) ELSE 0 END
  FROM legacy_login_links
 WHERE CAST(strftime('%s', substr(expires_at,1,19)) AS INTEGER) > unixepoch();

------------------------------------------------------------------ drop
-- Children before parents. The history of 001-028 goes with
-- legacy_schema_migrations: the files are gone, and migrate() records 29.
DROP TABLE legacy_api_key_models_off;
DROP TABLE legacy_account_models_off;
DROP TABLE legacy_model_roles;
DROP TABLE legacy_api_keys;
DROP TABLE legacy_accounts;
DROP TABLE legacy_usage_events;
DROP TABLE legacy_chat_titles;
DROP TABLE legacy_admin;
DROP TABLE legacy_settings;
DROP TABLE legacy_login_links;
DROP TABLE legacy_schema_migrations;
