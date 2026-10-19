-- Single-use sign-in links.
--
-- A link is a short random token, not an encoded credential: it expires, it is
-- spent on first use, and losing one costs a link rather than the admin key.
-- Stored here rather than in memory so a link can be minted from a shell,
-- which is the case that matters — a link made from a live session is no help
-- when you have no session.
CREATE TABLE IF NOT EXISTS login_links (
    token      TEXT PRIMARY KEY,
    key_id     TEXT NOT NULL REFERENCES api_keys (id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_login_links_expires ON login_links (expires_at);
