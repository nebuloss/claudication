-- One row per proxied request.
--
-- The relay already knows all of this; until now it only reached the log,
-- where nothing can aggregate it. A gateway whose whole job is to sit between
-- a client and a metered upstream should be able to answer "what did that
-- cost, and which key spent it" without grepping.
--
-- Deliberately an event table rather than running counters: counters cannot be
-- broken down after the fact, and a row per request is small enough that
-- retention, not size, is the thing to manage.
CREATE TABLE IF NOT EXISTS usage_events (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    at                 TEXT NOT NULL,
    -- Both are recorded loosely rather than as foreign keys: history must
    -- survive the key or the account it belongs to being deleted, and a
    -- cascade would erase exactly the record you go looking for afterwards.
    key_id             TEXT NOT NULL DEFAULT '',
    key_name           TEXT NOT NULL DEFAULT '',
    account_id         TEXT NOT NULL DEFAULT '',
    account_email      TEXT NOT NULL DEFAULT '',
    model              TEXT NOT NULL DEFAULT '',
    path               TEXT NOT NULL,
    status             INTEGER NOT NULL,
    streaming          INTEGER NOT NULL DEFAULT 0,
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    duration_ms        INTEGER NOT NULL DEFAULT 0,
    -- Empty on success. A stream that failed after its 200 lands here, which
    -- is the only way to tell it apart from one that worked.
    error              TEXT NOT NULL DEFAULT ''
);

-- Every query is "recent activity" or "this key, recently", in that order.
CREATE INDEX IF NOT EXISTS idx_usage_at ON usage_events (at DESC);
CREATE INDEX IF NOT EXISTS idx_usage_key_at ON usage_events (key_id, at DESC);
