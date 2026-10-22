-- Version 38 to 39: a client's agent may manage it, when the operator says so.
--
-- The gateway serves an MCP server on the relay (internal/httpapi/agent), so
-- the agent inside a client — Claude Code, Codex, crush, opencode — can read
-- what its client serves and, with this switch on, rebind its model names
-- and make its own update command. Every client starts with it off, the
-- existing ones included: nothing a model can do here was possible before,
-- and the operator is the one to decide it may.
--
-- Two more things the server needs, in the same step:
--
--   - clients.base, the address a client's machine was set up with, which
--     update_config writes into its files again rather than guess one from
--     the request. '' for every existing client: no fetch recorded it, and
--     the next command fetched from its page does.
--   - usage_events.kind 3, a change an agent made to its client, filed in the
--     request log where the operator reads what a client did, and counted as
--     neither a relayed request nor a refused one.
--
-- Rebuilt as 033 and 037 did, for the reason they give: a column added with
-- ALTER TABLE lands in SQLite's CREATE text without the comment schema.sql
-- gives it, a CHECK cannot be changed in place, and TestUpgradeMatchesFreshSchema
-- compares that text. The CREATE statements below are schema.sql's at
-- version 39, copied in; do not edit them, they are what this step made.
-- client_slots is clients' child, so it is set aside first and dropped
-- before it; usage_events is nobody's, and its indexes go first, since index
-- names are global.

------------------------------------------------------------------ set aside
ALTER TABLE client_slots RENAME TO legacy_client_slots;
ALTER TABLE clients      RENAME TO legacy_clients;
DROP INDEX idx_usage_at;
DROP INDEX idx_usage_conversation;
DROP INDEX idx_usage_error_code;
DROP INDEX idx_usage_rejected;
ALTER TABLE usage_events RENAME TO legacy_usage_events;

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
    fit_images    INTEGER NOT NULL DEFAULT 0,
    -- Whether the client's own agent may change it through the gateway's MCP
    -- server (internal/httpapi/agent): rebind its model names, add and
    -- remove its own, and make its update command. Off unless switched on:
    -- it hands a model the operator's choice of what the client runs.
    agent_manage  INTEGER NOT NULL DEFAULT 0,
    -- The address its machine was set up with: the base the last install or
    -- update command it fetched wrote into its files. The MCP server's
    -- update_config writes that one again rather than guess one from the
    -- request, which behind a proxy can name an address the client cannot
    -- reach. '' until a command is fetched.
    base          TEXT    NOT NULL DEFAULT ''
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

CREATE TABLE usage_events (
    id                 INTEGER PRIMARY KEY,
    -- When the request started.
    at                 INTEGER NOT NULL,
    -- 0 relayed for a client; 1 refused before the upstream, which spent
    -- nothing and has no key, model or account to group under; 2 made by the
    -- gateway for itself (store.InternalPath: naming a chat), part of a chat's
    -- cost but no evidence of who was having it; 3 a change a client's own
    -- agent made to it through the MCP server (internal/httpapi/agent): no
    -- request was relayed or refused, and it is in the log to be seen.
    -- Aggregates take kind IN (0, 2), the chat rollups kind = 0. Zero is the
    -- common case, so a caller that does not think about it files a relayed
    -- request.
    kind               INTEGER NOT NULL DEFAULT 0 CHECK (kind IN (0, 1, 2, 3)),
    key_id             TEXT    NOT NULL DEFAULT '',
    account_id         TEXT    NOT NULL DEFAULT '',
    -- Whose account served it: anthropic, openai. Kept with the row because,
    -- like the names, it has to outlive the account. Empty when no account
    -- did (031 filed every earlier relayed row as anthropic, the only one
    -- there was).
    provider           TEXT    NOT NULL DEFAULT '',
    -- The real model that served it: what was spent, and what reports group
    -- by. alias is what the client asked for when that was an alias.
    model              TEXT    NOT NULL DEFAULT '',
    alias              TEXT    NOT NULL DEFAULT '',
    path               TEXT    NOT NULL,
    -- The chat, as the client's own headers name it -- never derived from the
    -- messages, so the gateway groups requests without looking at content.
    -- Measured on the wire 2026-09-17: Claude Code sends
    -- X-Claude-Code-Session-Id, opencode and crush x-session-id, Codex
    -- session_id; internal/api/conversation.go has the table. Empty when the
    -- client sent nothing, never a guess: a fabricated grouping is
    -- indistinguishable from a true one once stored, and the empty bucket is
    -- how an operator finds a client that needs configuring.
    conversation_id    TEXT    NOT NULL DEFAULT '',
    -- The product token from the User-Agent. Stored because it cannot be
    -- recovered: the User-Agent is not kept, and x-session-id alone cannot
    -- tell opencode from crush.
    client             TEXT    NOT NULL DEFAULT '',
    -- How the client was run, where its User-Agent says: for Claude Code the
    -- entrypoint (cli, sdk-cli, sdk-ts...), then /client-app when the Agent
    -- SDK names one (internal/service/clients, Surface). Stored for the reason
    -- client is; it is what tells a headless chat, which Claude Code never
    -- names, from an interactive one not named yet.
    surface            TEXT    NOT NULL DEFAULT '',
    -- Which machine. For a refused request the only identity there is; for a
    -- relayed one, where it ran. Only as true as trusted-proxies makes it.
    ip                 TEXT    NOT NULL DEFAULT '',
    -- 0 is no answer at all.
    status             INTEGER NOT NULL,
    streaming          INTEGER NOT NULL DEFAULT 0,
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    -- The part of output_tokens spent reasoning, where the provider says
    -- (OpenAI does). Inside output_tokens already: shown, never added.
    reasoning_tokens   INTEGER NOT NULL DEFAULT 0,
    duration_ms        INTEGER NOT NULL DEFAULT 0,
    -- To the first event carrying content, stalled-and-retried attempts
    -- included; 0 is not measured. Duration alone cannot tell a long answer
    -- from an upstream that accepted the request and sat on it, which is what
    -- made crush report models "not responding" on 2026-10-01.
    first_token_ms     INTEGER NOT NULL DEFAULT 0,
    -- The failure in one word, decided once when recorded: the upstream's own
    -- type, content_check, no_answer, other. Stored because the text ends in a
    -- request_id, so no two are equal and it can be neither a filter nor a
    -- menu. Empty exactly when the request worked: RecordUsage fills it for
    -- every failure, so `error_code <> ''` is what an error is.
    error_code         TEXT    NOT NULL DEFAULT '',
    -- Empty on success. A stream that failed after its 200 lands here, which
    -- is the only way to tell it from one that worked.
    error              TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX idx_usage_at           ON usage_events (at);
CREATE INDEX idx_usage_conversation ON usage_events (conversation_id, at);
CREATE INDEX idx_usage_error_code   ON usage_events (error_code, at);
CREATE INDEX idx_usage_rejected     ON usage_events (at) WHERE kind = 1;

------------------------------------------------------------------ copy
INSERT INTO clients (key_id, type, mode, disabled_at, configured_at, name_chats, fit_images, agent_manage, base)
SELECT key_id, type, mode, disabled_at, configured_at, name_chats, fit_images, 0, ''
  FROM legacy_clients;

INSERT INTO client_slots (key_id, slot, name, model, follows, created_at, updated_at)
SELECT key_id, slot, name, model, follows, created_at, updated_at FROM legacy_client_slots;

INSERT INTO usage_events (id, at, kind, key_id, account_id, provider, model, alias, path, conversation_id, client, surface, ip, status, streaming, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, duration_ms, first_token_ms, error_code, error)
SELECT id, at, kind, key_id, account_id, provider, model, alias, path, conversation_id, client, surface, ip, status, streaming, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, duration_ms, first_token_ms, error_code, error
  FROM legacy_usage_events;

------------------------------------------------------------------ drop
DROP TABLE legacy_client_slots;
DROP TABLE legacy_clients;
DROP TABLE legacy_usage_events;
