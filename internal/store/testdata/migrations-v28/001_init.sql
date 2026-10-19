-- claudication state schema. Applied idempotently at startup.

-- Client credentials. The plaintext key is shown once at creation and never
-- stored; we keep sha256(key) plus a lookup prefix. Policy lives here rather
-- than in the config file so it can be changed without a restart.
CREATE TABLE IF NOT EXISTS api_keys (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    prefix       TEXT NOT NULL UNIQUE,
    hash         TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    last_used_at TEXT,
    revoked_at   TEXT,
    -- 0 means "inherit the global default from config".
    rpm_limit    INTEGER NOT NULL DEFAULT 0,
    -- 0 means unlimited.
    token_budget INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_api_keys_prefix ON api_keys (prefix);
