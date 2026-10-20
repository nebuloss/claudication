-- Version 36 to 37: fitting oversized images is a client's choice.
--
-- It was one gateway-wide switch, the passthrough.fit-oversized-images
-- setting. Whether a client sends enough screenshots to cross the upstream's
-- 20-image threshold, and whether the detail lost at 2000 px matters to it, is
-- a fact about the client, and one switch for all of them meant that turning
-- it on for one re-encoded every other's pictures too. So clients gains
-- fit_images, and the setting goes. Every client keeps what the gateway did
-- for it: on where the switch was on, off where it was off or never set (its
-- default).
--
-- A column added with ALTER TABLE lands in the CREATE text SQLite keeps
-- without the comment schema.sql gives it, and TestUpgradeMatchesFreshSchema
-- compares that text, so the table is rebuilt as 035 did it: set aside,
-- created, copied, dropped. The CREATE statements below are schema.sql's at
-- version 37, copied in; do not edit them, they are what this step made.
-- client_slots is clients' child, and renaming a parent re-points its children
-- at the new name, so it is set aside first, filled after clients and dropped
-- before it.

------------------------------------------------------------------ set aside
ALTER TABLE client_slots RENAME TO legacy_client_slots;
ALTER TABLE clients      RENAME TO legacy_clients;

------------------------------------------------------------------ create
CREATE TABLE clients (
    key_id      TEXT    PRIMARY KEY REFERENCES api_keys (id) ON DELETE CASCADE,
    type        TEXT    NOT NULL,
    -- How it names models: binding, its own slot names (every typed client);
    -- direct, real model ids less api_key_models_off (a custom one may).
    -- Switching keeps both: the slots and the switches stay stored.
    mode        TEXT    NOT NULL DEFAULT 'binding' CHECK (mode IN ('binding', 'direct')),
    -- Set while the operator has the client switched off: its key is refused
    -- before anything is routed.
    disabled_at   INTEGER,
    -- When the client was first set up with its key: its install command
    -- fetched, or its first request made. The page stops offering the key
    -- and the install command from then on, until the key is regenerated,
    -- which clears it. Stored rather than read off the request log, which
    -- prunes and can be switched off.
    configured_at INTEGER,
    -- Whether its chats are named: read off the client's own naming request
    -- for a type that names its sessions, asked for under its own key on the
    -- chat's own model for any other. On unless switched off.
    name_chats    INTEGER NOT NULL DEFAULT 1,
    -- Whether a request of its carrying more than 20 images has every image
    -- capped at 2000 px, which Claude refuses it over otherwise
    -- (internal/relay/passes, ShrinkImages). Off unless switched on: it
    -- re-encodes the client's own pictures.
    fit_images    INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;

CREATE TABLE client_slots (
    key_id     TEXT    NOT NULL REFERENCES clients (key_id) ON DELETE CASCADE,
    slot       TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    model      TEXT    NOT NULL DEFAULT '',
    follows    TEXT    NOT NULL DEFAULT '',
    -- The order a custom client's own slots were added in.
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (key_id, slot),
    UNIQUE (key_id, name),
    CHECK ((model = '') <> (follows = ''))
) WITHOUT ROWID;

------------------------------------------------------------------ copy
INSERT INTO clients (key_id, type, mode, disabled_at, configured_at, name_chats, fit_images)
SELECT key_id, type, mode, disabled_at, configured_at, name_chats,
       COALESCE((SELECT value = 'true' FROM settings
                  WHERE key = 'passthrough.fit-oversized-images'), 0)
  FROM legacy_clients;

INSERT INTO client_slots (key_id, slot, name, model, follows, created_at, updated_at)
SELECT key_id, slot, name, model, follows, created_at, updated_at FROM legacy_client_slots;

DELETE FROM settings WHERE key = 'passthrough.fit-oversized-images';

------------------------------------------------------------------ drop
DROP TABLE legacy_client_slots;
DROP TABLE legacy_clients;
