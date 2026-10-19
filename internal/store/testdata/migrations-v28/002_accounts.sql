-- Upstream accounts: one row per logged-in provider account.
--
-- Tokens are sealed with the instance key before they reach this table, so
-- they are stored as BLOBs, never as readable text.
CREATE TABLE IF NOT EXISTS accounts (
    id              TEXT PRIMARY KEY,
    provider        TEXT NOT NULL,
    email           TEXT NOT NULL,
    account_uuid    TEXT NOT NULL DEFAULT '',
    access_token    BLOB NOT NULL,
    refresh_token   BLOB NOT NULL,
    expires_at      TEXT NOT NULL,
    created_at      TEXT NOT NULL,
    last_refresh_at TEXT,
    last_used_at    TEXT,
    last_error      TEXT,
    disabled_at     TEXT,
    -- Identity is established once, at login, and never rewritten by a token
    -- refresh. auth2api derives the email from every refresh response, so a
    -- refresh that omits the account object renames the row to "unknown",
    -- orphaning its stats and spawning a phantom duplicate on restart.
    UNIQUE (provider, email)
);

CREATE INDEX IF NOT EXISTS idx_accounts_provider ON accounts (provider);

-- Admin-scoped credentials may drive the admin API and the UI.
ALTER TABLE api_keys ADD COLUMN is_admin INTEGER NOT NULL DEFAULT 0;
