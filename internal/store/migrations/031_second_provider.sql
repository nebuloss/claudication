-- Version 30 to 31: a second provider.
--
-- accounts: the account is (provider, email, account_uuid) rather than
-- (provider, email), because one ChatGPT sign-in reaches a personal workspace
-- and a Team one under the same address, each with its own
-- chatgpt_account_id and its own bill; and plan, the subscription the
-- sign-in named. usage_events: provider, whose account served a request,
-- which has to outlive the account as the names do; and reasoning_tokens,
-- the part of output OpenAI reports spent reasoning.
--
-- SQLite changes neither a UNIQUE nor a column's place without a rebuild, and
-- TestUpgradeMatchesFreshSchema compares the text it keeps of each table, so
-- both are rebuilt as schema.sql has them: set aside, created, copied,
-- dropped. The CREATE statements below are schema.sql's at version 31, copied
-- in; do not edit them, they are what this step made.
--
-- Inside migrate()'s transaction with foreign_keys=ON, as 029 was, and in its
-- order for the same reason: renaming a parent re-points its children's
-- REFERENCES at the new name, so account_quota and account_models_off are set
-- aside with accounts, filled after it, and dropped before it. usage_events
-- is nobody's parent or child. Index names are global, so its go first.

------------------------------------------------------------------ set aside
DROP INDEX idx_usage_at;
DROP INDEX idx_usage_conversation;
DROP INDEX idx_usage_error_code;
DROP INDEX idx_usage_rejected;
ALTER TABLE usage_events       RENAME TO legacy_usage_events;
ALTER TABLE account_models_off RENAME TO legacy_account_models_off;
ALTER TABLE account_quota      RENAME TO legacy_account_quota;
ALTER TABLE accounts           RENAME TO legacy_accounts;

------------------------------------------------------------------ create
CREATE TABLE accounts (
    id                 TEXT    PRIMARY KEY,
    provider           TEXT    NOT NULL,
    email              TEXT    NOT NULL,
    -- The provider's own id for the account. For ChatGPT it is the
    -- chatgpt_account_id every request carries, naming the workspace billed.
    account_uuid       TEXT    NOT NULL DEFAULT '',
    -- The subscription, in the provider's word (ChatGPT's chatgpt_plan_type:
    -- plus, pro, team), as the sign-in said; '' when it did not.
    plan               TEXT    NOT NULL DEFAULT '',
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
    --
    -- The provider's account id is part of it (031): one ChatGPT sign-in
    -- reaches a personal workspace and a Team one under the same address,
    -- each billed apart, and they are two accounts.
    UNIQUE (provider, email, account_uuid)
);

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
    -- The provider's own rows: for Anthropic the normalised `limits` array
    -- verbatim, per-model weekly rows included, for the UI to render exactly
    -- what `/usage` prints; for ChatGPT one per window, by its length.
    detail           TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE account_models_off (
    account_id TEXT    NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    model      TEXT    NOT NULL,
    off_at     INTEGER NOT NULL,
    PRIMARY KEY (account_id, model)
) WITHOUT ROWID;

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
    -- Whose account served it: anthropic, openai. Kept with the row because,
    -- like the names, it has to outlive the account. Empty when no account
    -- did (031 filed every earlier relayed row as anthropic, the only one
    -- there was).
    provider           TEXT    NOT NULL DEFAULT '',
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
    -- The part of output_tokens spent reasoning, where the provider says
    -- (OpenAI does). Inside output_tokens already: shown, never added.
    reasoning_tokens   INTEGER NOT NULL DEFAULT 0,
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
CREATE INDEX idx_usage_at           ON usage_events (at);
CREATE INDEX idx_usage_conversation ON usage_events (conversation_id, at);
CREATE INDEX idx_usage_error_code   ON usage_events (error_code, at);
CREATE INDEX idx_usage_rejected     ON usage_events (at) WHERE kind = 1;

------------------------------------------------------------------ copy
-- Every account keeps its id, so its quota, its switches and its history
-- stay its own. Its plan is unknown until it signs in again.
INSERT INTO accounts (id, provider, email, account_uuid, plan, access_token, refresh_token, expires_at,
                      refresh_expires_at, created_at, last_refresh_at, last_used_at, last_error,
                      disabled_at, refresh_dead_at, position)
SELECT id, provider, email, account_uuid, '', access_token, refresh_token, expires_at,
       refresh_expires_at, created_at, last_refresh_at, last_used_at, last_error,
       disabled_at, refresh_dead_at, position
  FROM legacy_accounts;

INSERT INTO account_quota (account_id, updated_at, five_hour_util, five_hour_reset, five_hour_status,
                           seven_day_util, seven_day_reset, seven_day_status, detail)
SELECT account_id, updated_at, five_hour_util, five_hour_reset, five_hour_status,
       seven_day_util, seven_day_reset, seven_day_status, detail
  FROM legacy_account_quota;

INSERT INTO account_models_off (account_id, model, off_at)
SELECT account_id, model, off_at FROM legacy_account_models_off;

-- Anthropic was the only provider before this step, so every row an account
-- served was served by one of its accounts.
INSERT INTO usage_events (id, at, kind, key_id, account_id, provider, model, alias, path, conversation_id,
                          client, ip, status, streaming, input_tokens, output_tokens, cache_read_tokens,
                          cache_write_tokens, reasoning_tokens, duration_ms, first_token_ms, error_code, error)
SELECT id, at, kind, key_id, account_id, CASE WHEN account_id <> '' THEN 'anthropic' ELSE '' END,
       model, alias, path, conversation_id, client, ip, status, streaming, input_tokens, output_tokens,
       cache_read_tokens, cache_write_tokens, 0, duration_ms, first_token_ms, error_code, error
  FROM legacy_usage_events;

------------------------------------------------------------------ drop
DROP TABLE legacy_account_models_off;
DROP TABLE legacy_account_quota;
DROP TABLE legacy_accounts;
DROP TABLE legacy_usage_events;
