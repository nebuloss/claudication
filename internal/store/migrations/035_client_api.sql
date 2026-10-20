-- Version 34 to 35: a client's API is its type's.
--
-- clients loses dialect. With Messages reaching ChatGPT as Responses reaches
-- Claude, the API a client was pointed at no longer decided which models it
-- could be served, and the choice was left only to confuse: each type speaks
-- its own (claude-code, crush and opencode Messages; codex Responses), and a
-- custom client's key works on both. Nothing carries the column over. A
-- client made on its type's other API -- an opencode on Responses, a crush on
-- Responses -- is on its native one from here, and its configuration has to
-- be installed again from its page.
--
-- Its slot names stay as they were but for one mark. opencode on Messages
-- needs "claude" in a model's name: its Anthropic provider turns tool-call
-- streaming off for any id without it (opencode 1.17.13's bundled provider
-- code), and an opencode made on Responses was given names without it
-- (large, small). So an opencode slot whose name lacks it is given the name a
-- new one would have, claude- in front, unless that name is already the
-- client's or would pass the 64 characters a name may have; such a slot is
-- left for the operator to rename, and its page says the mark is missing.
-- crush's gpt-5 mark was for Responses only and is left, being harmless.
--
-- SQLite drops a column by editing the CREATE text it keeps, comments and
-- all, and TestUpgradeMatchesFreshSchema compares that text, so the table is
-- rebuilt as schema.sql has it, as 031 did: set aside, created, copied,
-- dropped. The CREATE statements below are schema.sql's at version 35, copied
-- in; do not edit them, they are what this step made. client_slots is
-- clients' child, and renaming a parent re-points its children at the new
-- name, so it is set aside first, filled after clients and dropped before it.

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
    name_chats    INTEGER NOT NULL DEFAULT 1
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
INSERT INTO clients (key_id, type, mode, disabled_at, configured_at, name_chats)
SELECT key_id, type, mode, disabled_at, configured_at, name_chats FROM legacy_clients;

INSERT INTO client_slots (key_id, slot, name, model, follows, created_at, updated_at)
SELECT key_id, slot, name, model, follows, created_at, updated_at FROM legacy_client_slots;

UPDATE client_slots SET name = 'claude-' || name
 WHERE instr(name, 'claude') = 0
   AND length(name) <= 57
   AND key_id IN (SELECT key_id FROM clients WHERE type = 'opencode')
   AND NOT EXISTS (SELECT 1 FROM client_slots AS taken
                    WHERE taken.key_id = client_slots.key_id
                      AND taken.name = 'claude-' || client_slots.name);

------------------------------------------------------------------ drop
DROP TABLE legacy_client_slots;
DROP TABLE legacy_clients;
