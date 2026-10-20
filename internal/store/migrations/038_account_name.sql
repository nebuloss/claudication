-- Version 37 to 38: an account can be named.
--
-- accounts gains name, what the operator calls the account where its address
-- does not say: two seats can share an address, and one address can be a
-- personal subscription and a work one. '' is unnamed and shown as the
-- address, so nothing is backfilled: every account keeps reading as it did.
--
-- SQLite adds a column by editing the CREATE text it keeps, at the end of the
-- column list, and TestUpgradeMatchesFreshSchema compares that text; the
-- column belongs beside the address it stands in for, so the table is rebuilt
-- as schema.sql has it, as 031 did: set aside, created, copied, dropped. The
-- CREATE statements below are schema.sql's at version 38, copied in; do not
-- edit them, they are what this step made. Renaming a parent re-points its
-- children's REFERENCES at the new name, so account_quota, account_models_off
-- and account_model_tests are set aside with accounts, filled after it and
-- dropped before it.
--
-- The names ledger needs nothing: it holds what an account is called, and an
-- unnamed account is called its address, which is what it holds already.

------------------------------------------------------------------ set aside
ALTER TABLE account_model_tests RENAME TO legacy_account_model_tests;
ALTER TABLE account_models_off  RENAME TO legacy_account_models_off;
ALTER TABLE account_quota       RENAME TO legacy_account_quota;
ALTER TABLE accounts            RENAME TO legacy_accounts;

------------------------------------------------------------------ create
CREATE TABLE accounts (
    id                 TEXT    PRIMARY KEY,
    provider           TEXT    NOT NULL,
    email              TEXT    NOT NULL,
    -- What the operator calls it, where it is not its address: a personal
    -- subscription and a work one can share one, and an address says nothing
    -- of which seat it is. '' is unnamed, shown as the address (038).
    name               TEXT    NOT NULL DEFAULT '',
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

CREATE TABLE account_model_tests (
    account_id TEXT    NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    model      TEXT    NOT NULL,
    at         INTEGER NOT NULL,
    ok         INTEGER NOT NULL,
    status     INTEGER NOT NULL DEFAULT 0,
    latency_ms INTEGER NOT NULL DEFAULT 0,
    error      TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (account_id, model)
) WITHOUT ROWID;

------------------------------------------------------------------ copy
-- Every account keeps its id, so its quota, its switches, its tests and its
-- history stay its own.
INSERT INTO accounts (id, provider, email, name, account_uuid, plan, access_token, refresh_token,
                      expires_at, refresh_expires_at, created_at, last_refresh_at, last_used_at,
                      last_error, disabled_at, refresh_dead_at, position)
SELECT id, provider, email, '', account_uuid, plan, access_token, refresh_token,
       expires_at, refresh_expires_at, created_at, last_refresh_at, last_used_at,
       last_error, disabled_at, refresh_dead_at, position
  FROM legacy_accounts;

INSERT INTO account_quota (account_id, updated_at, five_hour_util, five_hour_reset, five_hour_status,
                           seven_day_util, seven_day_reset, seven_day_status, detail)
SELECT account_id, updated_at, five_hour_util, five_hour_reset, five_hour_status,
       seven_day_util, seven_day_reset, seven_day_status, detail
  FROM legacy_account_quota;

INSERT INTO account_models_off (account_id, model, off_at)
SELECT account_id, model, off_at FROM legacy_account_models_off;

INSERT INTO account_model_tests (account_id, model, at, ok, status, latency_ms, error)
SELECT account_id, model, at, ok, status, latency_ms, error FROM legacy_account_model_tests;

------------------------------------------------------------------ drop
DROP TABLE legacy_account_model_tests;
DROP TABLE legacy_account_models_off;
DROP TABLE legacy_account_quota;
DROP TABLE legacy_accounts;
