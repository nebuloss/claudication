-- Version 32 to 33: usage_events.surface, how a client was run.
--
-- Claude Code's User-Agent names its entrypoint — cli for the interactive
-- REPL, sdk-cli for claude -p, sdk-ts and sdk-py for the Agent SDK — and the
-- User-Agent is not kept, so a column is the only way to remember it. It is
-- what tells a headless chat, which Claude Code never names, from an
-- interactive one that has not been named yet (internal/service/clients,
-- Surface). Every earlier row has none: what it was run as is gone.
--
-- Beside client, which it qualifies, rather than at the end: SQLite adds a
-- column only at the end, and TestUpgradeMatchesFreshSchema compares the text
-- it keeps of each table, so usage_events is rebuilt as 031 rebuilt it: set
-- aside, created as schema.sql has it, copied, dropped. The CREATE statement
-- below is schema.sql's at version 33, copied in; do not edit it, it is what
-- this step made. usage_events is nobody's parent or child, so nothing is
-- re-pointed; index names are global, so its go first.

------------------------------------------------------------------ set aside
DROP INDEX idx_usage_at;
DROP INDEX idx_usage_conversation;
DROP INDEX idx_usage_error_code;
DROP INDEX idx_usage_rejected;
ALTER TABLE usage_events RENAME TO legacy_usage_events;

------------------------------------------------------------------ create
CREATE TABLE usage_events (
    id                 INTEGER PRIMARY KEY,
    -- When the request started.
    at                 INTEGER NOT NULL,
    -- 0 relayed for a client; 1 refused before the upstream, which spent
    -- nothing and has no key, model or account to group under; 2 made by the
    -- gateway for itself (store.InternalPath: naming a chat), part of a chat's
    -- cost but no evidence of who was having it. Aggregates take kind <> 1,
    -- the chat rollups kind = 0. Zero is the common case, so a caller that
    -- does not think about it files a relayed request.
    kind               INTEGER NOT NULL DEFAULT 0 CHECK (kind IN (0, 1, 2)),
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
INSERT INTO usage_events (id, at, kind, key_id, account_id, provider, model, alias, path, conversation_id,
                          client, ip, status, streaming, input_tokens, output_tokens, cache_read_tokens,
                          cache_write_tokens, reasoning_tokens, duration_ms, first_token_ms, error_code, error)
SELECT id, at, kind, key_id, account_id, provider, model, alias, path, conversation_id,
       client, ip, status, streaming, input_tokens, output_tokens, cache_read_tokens,
       cache_write_tokens, reasoning_tokens, duration_ms, first_token_ms, error_code, error
  FROM legacy_usage_events;

------------------------------------------------------------------ drop
DROP TABLE legacy_usage_events;
