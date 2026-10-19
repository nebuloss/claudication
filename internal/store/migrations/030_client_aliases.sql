-- Version 29 to 30: model aliases bound per client.
--
-- model_roles gains client, the client a binding is for, and a CHECK that a
-- row is gateway-wide, one key's or one client's (see schema.sql). SQLite adds
-- a column but not a CHECK, so the table is rebuilt: set aside, created as
-- schema.sql has it, copied, dropped. Every existing row is gateway-wide or a
-- key's, and keeps being that with client NULL.
--
-- model_roles is a child of api_keys and nothing's parent, so renaming it
-- re-points nothing, and the copy cannot leave a key dangling that was not
-- dangling before. The index names are global, so the old ones go first.

DROP INDEX model_roles_global;
DROP INDEX model_roles_key;
ALTER TABLE model_roles RENAME TO legacy_model_roles;

CREATE TABLE model_roles (
    key_id     TEXT    REFERENCES api_keys (id) ON DELETE CASCADE,
    client     TEXT,
    role       TEXT    NOT NULL,
    model      TEXT    NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL,
    CHECK (key_id IS NULL OR client IS NULL)
);
-- One binding per role at each level. Partial indexes, because NULLs are
-- distinct in a plain UNIQUE (key_id, client, role).
CREATE UNIQUE INDEX model_roles_global ON model_roles (role) WHERE key_id IS NULL AND client IS NULL;
CREATE UNIQUE INDEX model_roles_key    ON model_roles (key_id, role) WHERE key_id IS NOT NULL;
CREATE UNIQUE INDEX model_roles_client ON model_roles (client, role) WHERE client IS NOT NULL;

INSERT INTO model_roles (key_id, client, role, model, updated_at)
SELECT key_id, NULL, role, model, updated_at FROM legacy_model_roles;

DROP TABLE legacy_model_roles;
