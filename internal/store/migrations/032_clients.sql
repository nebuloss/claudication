-- Version 31 to 32: clients, and no more gateway-wide model aliases.
--
-- A key becomes what it is for: a client, of a type the gateway writes
-- configurations for, with model names of its own (clients and client_slots,
-- see schema.sql). Every key there is becomes a custom client in direct mode
-- under its own name, keeping its id -- so its secret, limits, switched-off
-- models and history are the client's with nothing copied. The gateway's own
-- titling key, which the settings row chat.titles.key names, is deleted: see
-- below.
--
-- The aliases go (model_roles), with every binding of them: gateway-wide, per
-- key and per User-Agent. Nothing carries them into the new tables. A client
-- that asked for claude-large is set up again, from its page, with names of
-- its own; the owner chose that over keeping a second way of naming models
-- alive for the configurations already written.
--
-- And the two API switches become one gateway switch: off only if both were
-- off, since with either on the gateway was serving.
--
-- The CREATE statements below are schema.sql's at version 32, copied in; do
-- not edit them, they are what this step made.

------------------------------------------------------------------ clients
CREATE TABLE clients (
    key_id      TEXT    PRIMARY KEY REFERENCES api_keys (id) ON DELETE CASCADE,
    type        TEXT    NOT NULL,
    -- The API its configuration points at: anthropic (Messages) or openai
    -- (Responses), as the gateway's protocols are named.
    dialect     TEXT    NOT NULL,
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

-- The gateway's own titling key goes: a chat is named under its client's own
-- key now, or not at all, so nothing authenticates as the gateway any more.
-- Its history stays, named through the names table as any deleted key's is.
DELETE FROM api_keys WHERE id IN (SELECT value FROM settings WHERE key = 'chat.titles.key');

-- Every key left becomes a custom client in direct mode, which is what it was:
-- a key naming real models. Each names its chats as the gateway-wide switch
-- did — on only where it was switched on, its default having been off — so
-- an upgrade starts no titling the operator had not asked for. A custom
-- client names nothing itself, so the capture switch has nothing to carry.
INSERT INTO clients (key_id, type, dialect, mode, configured_at, name_chats)
SELECT id, 'custom', 'anthropic', 'direct', last_used_at,
       COALESCE((SELECT value = 'true' FROM settings WHERE key = 'chat.titles.enabled'), 0)
  FROM api_keys;

-- The titling switches go: naming chats is each client's setting.
DELETE FROM settings WHERE key IN ('chat.titles.key', 'chat.titles.enabled', 'chat.titles.capture');

DROP INDEX model_roles_global;
DROP INDEX model_roles_key;
DROP INDEX model_roles_client;
DROP TABLE model_roles;

-- The last test of each model on each account: the account page's Models
-- section runs one tiny turn per model on that account's own credential (no
-- client's key, and no row in the request log: it is no client's traffic),
-- and shows the answer after a reload. One row per pair, replaced each time.
-- error is the upstream's own, empty when the turn was answered.
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

------------------------------------------------------------------ the switch
INSERT INTO settings (key, value, updated_at)
SELECT 'gateway.enabled', 'false', CAST(strftime('%s', 'now') AS INTEGER) * 1000
 WHERE (SELECT COUNT(*) FROM settings
         WHERE key IN ('api.anthropic.enabled', 'api.openai.enabled') AND value = 'false') = 2;
DELETE FROM settings WHERE key IN ('api.anthropic.enabled', 'api.openai.enabled');
