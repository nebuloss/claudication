-- The admin account. There is exactly one, and the CHECK says so rather than
-- leaving it to convention: this is an operator's own gateway, not a
-- multi-tenant service, and a second row would be a bug rather than a feature.
CREATE TABLE IF NOT EXISTS admin (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    password_hash TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

-- Sign-in links no longer belong to an API key.
--
-- They used to reference an admin-scoped key, back when the UI authenticated
-- with one. With a single password account there is nothing to reference: a
-- link is simply a pass to the admin. Unspent links are ephemeral by design, so
-- dropping the old table costs nothing worth migrating.
DROP TABLE IF EXISTS login_links;

CREATE TABLE IF NOT EXISTS login_links (
    token      TEXT PRIMARY KEY,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_login_links_expires ON login_links (expires_at);

-- API keys no longer carry an admin scope.
--
-- The scope existed so an admin-marked key could drive the admin API before
-- there was an account to sign in to. With a password there is one way to
-- become admin, and a password change revokes it — which is precisely what a
-- second, key-shaped path could not offer. Keys are now client credentials
-- only, and the column would be a footgun left loaded.
ALTER TABLE api_keys DROP COLUMN is_admin;
