-- claudication's schema, whole: what a fresh database is created with, and
-- what migrations/ brings an older one to. Version 33 (schemaVersion in
-- store.go).
--
-- Written as if from scratch. Migrations 001-028 grew it a column at a time;
-- their reasoning is kept here beside what it explains, and the files are gone
-- (see migrate). TestUpgradeMatchesFreshSchema holds this file and the upgrade
-- path to the same result.
--
-- Every moment is INTEGER unix milliseconds, UTC. There used to be three kinds
-- -- RFC3339 at whole seconds, fixed-width nine-digit text, unix seconds -- and
-- text only compares as time at a fixed width, which once let a window that
-- started on a whole second drop the events inside that second (020). Text
-- times also took 30 bytes in every index that led with them: usage_events had
-- five such indexes, and the indexes were bigger than the table.
--
-- No AUTOINCREMENT anywhere. Pruning deletes the oldest rows, so the highest
-- rowid survives and new ids keep rising without it, and the sqlite_sequence
-- write it adds to every insert bought nothing.

-- One row: the version this file is at, and when the database reached it.
-- version is the highest step applied; see migrate.
CREATE TABLE schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);

------------------------------------------------------------------ api_keys
-- Client credentials. Policy lives here rather than in the config file so it
-- changes without a restart. There is no revocation: it was one-way, so it was
-- a delete that left a row behind; deleting is the one way to withdraw a key.
CREATE TABLE api_keys (
    id            TEXT    PRIMARY KEY,
    name          TEXT    NOT NULL,
    -- The first characters of the random part, in the clear, to find the row.
    -- Not a secret: the prefix alone proves nothing.
    prefix        TEXT    NOT NULL UNIQUE,
    -- sha256 of the key, which is what authenticates: a constant-time compare
    -- that needs no sealing key on the request path.
    hash          TEXT    NOT NULL,
    -- The secret, sealed like the account tokens (internal/secret), so a key's
    -- client configs can be shown again. The usual rule -- keep only the hash
    -- -- bought nothing here: the same file holds every account's OAuth
    -- tokens, worth more than any key, under the same seal, so a stolen file
    -- is enough for neither and the state directory is enough for both.
    -- NULL only for the gateway's own titling key, whose secret is thrown away
    -- by design; ReissueUnkeptKeys gives any other NULL a new secret.
    sealed_secret BLOB,
    created_at    INTEGER NOT NULL,
    last_used_at  INTEGER,
    -- Requests per rate_period_s; 0 is the gateway's default. A period rather
    -- than a fixed minute because "two hundred an hour" is not "three a
    -- minute": the second refuses a burst of ten the first permits.
    rate_limit    INTEGER NOT NULL DEFAULT 0  CHECK (rate_limit >= 0),
    rate_period_s INTEGER NOT NULL DEFAULT 60 CHECK (rate_period_s > 0),
    -- Tokens per rolling day; 0 is unlimited.
    token_budget  INTEGER NOT NULL DEFAULT 0  CHECK (token_budget >= 0)
);

------------------------------------------------------------------ clients
-- What a key is for: a client the operator set up -- "laptop", "work crush" --
-- of a type the gateway writes configurations for (internal/service/clients:
-- claude-code, codex, opencode, crush, or custom), pointed at one of its two
-- APIs. One client holds exactly one key, so the client's id IS its key's id
-- and this is a table beside api_keys rather than columns in it: everything
-- that already names a key -- usage_events, api_key_models_off, the names
-- table -- names the client with no second id to keep in step, and the
-- gateway's own titling key, which is no client, is simply the key with no
-- row here. api_keys stays the credential and its limits, read on every
-- request; this is what the admin UI and the config writer read.
--
-- Every key there was became a custom client at 032, under its own name.
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

-- A direct-mode client's models switched off. Kept as what is off, so a model
-- released tomorrow is usable until someone decides otherwise. model is as the
-- operator switched it; requests are matched by store.ModelKey, so a dated id
-- and its short form are one model. A binding-mode client keeps its rows, for
-- when it is switched back, and is not held to them: it names its slots.
CREATE TABLE api_key_models_off (
    key_id TEXT    NOT NULL REFERENCES api_keys (id) ON DELETE CASCADE,
    model  TEXT    NOT NULL,
    off_at INTEGER NOT NULL,
    PRIMARY KEY (key_id, model)
) WITHOUT ROWID;

-- A client's model slots: the model fields its configuration has (crush's
-- large and small, Codex's model and review_model...), each under a model name
-- its configuration asks for. A name means something only for its own
-- client's key -- two clients may each have a "large" -- so it is unique
-- within the client and nowhere else, and resolving one is a read of the
-- requesting key's own rows. This replaced the gateway-wide aliases
-- (claude-large and the rest, model_roles until 032): one name bound in one
-- place for every client meant moving one client moved them all.
--
-- A slot is bound to exactly one of: model, a real model of any provider; or
-- follows, a moving target -- claude:<family>, the newest of a Claude family;
-- openai:first and openai:light, the ChatGPT backend's ranking -- which is
-- what the built-in aliases used to follow (internal/service/clients,
-- Follow). Deleting the client deletes its slots, by the cascade.
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

------------------------------------------------------------------ accounts
-- Upstream accounts, one row per logged-in provider account. Tokens are sealed
-- before they reach this table, so they are BLOBs, never readable text.
CREATE TABLE accounts (
    id                 TEXT    PRIMARY KEY,
    provider           TEXT    NOT NULL,
    email              TEXT    NOT NULL,
    -- The provider's own id for the account. For ChatGPT it is the
    -- chatgpt_account_id every request carries, naming the workspace billed.
    account_uuid       TEXT    NOT NULL DEFAULT '',
    -- The subscription, in the provider's word (ChatGPT's chatgpt_plan_type:
    -- plus, pro, team), as the sign-in said; '' when it did not.
    plan               TEXT    NOT NULL DEFAULT '',
    access_token       BLOB    NOT NULL,
    refresh_token      BLOB    NOT NULL,
    expires_at         INTEGER NOT NULL,
    -- When re-authorisation becomes unavoidable: the refresh token has its own
    -- lifetime, which refreshing cannot extend. Claude Code reads the same
    -- value for "Your login expires in N days" and warns three days out. NULL
    -- is the provider not saying, which is not the same as expired.
    refresh_expires_at INTEGER,
    created_at         INTEGER NOT NULL,
    last_refresh_at    INTEGER,
    last_used_at       INTEGER,
    last_error         TEXT    NOT NULL DEFAULT '',
    disabled_at        INTEGER,
    -- When the refresh token was refused as invalid_grant: revoked, expired or
    -- spent, and nothing about it improves with time. The durable version of
    -- the set Claude Code keeps of such tokens. Not a wipe of the credentials,
    -- as the client does: a gateway has to say which of several accounts needs
    -- reconnecting, and the access token may have free hours left. Cleared by
    -- a successful consent flow.
    refresh_dead_at    INTEGER,
    -- The operator's priority order, lowest first. Which account serves is a
    -- decision, not an inference: a personal subscription and a work one are
    -- not interchangeable. Quota is a reason to skip, not the ordering.
    position           INTEGER NOT NULL DEFAULT 0,
    -- Identity is set once, at login, and never rewritten by a refresh.
    -- auth2api derives the email from every refresh response, so a refresh
    -- that omits the account object renames the row to "unknown", orphaning
    -- its stats and spawning a phantom duplicate on restart.
    --
    -- The provider's account id is part of it (031): one ChatGPT sign-in
    -- reaches a personal workspace and a Team one under the same address,
    -- each billed apart, and they are two accounts.
    UNIQUE (provider, email, account_uuid)
);

-- What the provider last said about an account's subscription windows. For
-- Anthropic, /api/oauth/usage: the figures `/usage` prints in the client,
-- polled rather than read off relayed responses' rate limit headers, which
-- showed nothing until an account had served a request and carried only the
-- two headline windows. For ChatGPT, /wham/usage and the x-codex-* headers
-- every answer carries. The windows are put in the columns by their length.
--
-- Its own table because the poller is its only writer and it changes every
-- poll, while the row beside it holds the credentials. No row is never
-- fetched, which is not zero: an account not yet heard from is unknown, not
-- idle, and the two route differently. A NULL util is a window the endpoint
-- did not report.
CREATE TABLE account_quota (
    account_id       TEXT    PRIMARY KEY REFERENCES accounts (id) ON DELETE CASCADE,
    updated_at       INTEGER NOT NULL,
    -- Percentages, 0-100.
    five_hour_util   REAL,
    five_hour_reset  INTEGER,
    five_hour_status TEXT    NOT NULL DEFAULT '',
    seven_day_util   REAL,
    seven_day_reset  INTEGER,
    seven_day_status TEXT    NOT NULL DEFAULT '',
    -- The provider's own rows: for Anthropic the normalised `limits` array
    -- verbatim, per-model weekly rows included, for the UI to render exactly
    -- what `/usage` prints; for ChatGPT one per window, by its length.
    detail           TEXT    NOT NULL DEFAULT ''
);

-- The models an operator turned off for an account, by the rule
-- api_key_models_off follows: a row is an off, no row is on.
CREATE TABLE account_models_off (
    account_id TEXT    NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    model      TEXT    NOT NULL,
    off_at     INTEGER NOT NULL,
    PRIMARY KEY (account_id, model)
) WITHOUT ROWID;

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

------------------------------------------------------------------ names
-- What a key or an account was last called, outliving it. History must
-- survive a key or an account being deleted, so usage_events holds their ids
-- loosely, with no foreign key -- a cascade would erase exactly the record
-- looked for afterwards. It used to repeat key_name and account_email on every
-- row for the same reason, and every reader wanted the latest name anyway,
-- found with a correlated ORDER BY at DESC LIMIT 1 per id. So a rename shows
-- across all of history.
--
-- Written when a key or an account is created or renamed, never on the request
-- path. Pruned with the chat titles once nothing refers to an id.
CREATE TABLE names (
    kind TEXT NOT NULL CHECK (kind IN ('key', 'account')),
    id   TEXT NOT NULL,
    name TEXT NOT NULL,
    PRIMARY KEY (kind, id)
) WITHOUT ROWID;

------------------------------------------------------------------ usage_events
-- One row per request: an event table rather than counters, because counters
-- cannot be broken down after the fact, and a row per request is small enough
-- that retention, not size, is what to manage.
--
-- Requests the gateway refused are rows here too. A refused request is a
-- relayed one with its key, model and tokens missing, and the read that matters
-- -- one time-ordered, keyset-paged, filtered stream -- is what two tables would
-- break.
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
-- Ascending, not DESC. The rowid rides at the end of every index entry,
-- ascending, so a backwards walk of (at) yields (at DESC, id DESC): the
-- request log's ORDER BY, with no sort. (at DESC) yielded (at DESC, id ASC)
-- and needed a temp B-tree for the last term. Serves every window: reports,
-- budgets, the prune, the unfiltered log.
CREATE INDEX idx_usage_at           ON usage_events (at);
-- A chat's rows: the chat detail, the rollup's per-chat subqueries,
-- ChatContexts, the unattributed bucket (conversation_id = ''). Deliberately
-- not partial: the planner cannot prove a bound ? is non-empty, so a partial
-- index could not serve `conversation_id = ?`, and forgetting the guard once
-- cost 2.6 s per chat list. Full measured 12.1 MB against 10.9 MB partial at
-- 300k rows.
CREATE INDEX idx_usage_conversation ON usage_events (conversation_id, at);
-- The Message filter, and its menu as a covering scan.
CREATE INDEX idx_usage_error_code   ON usage_events (error_code, at);
-- The refused requests, for Forwarded = no: a few per cent of rows, or a flood,
-- and either way only read newest first.
CREATE INDEX idx_usage_rejected     ON usage_events (at) WHERE kind = 1;
-- No index on key_id or account_id. Every statement that touches them groups
-- over a window rather than ranging on one id; 006's (key_id, at) was used
-- only as a full scan under a temp B-tree, and cost ~22 MB per 300k events
-- plus a B-tree insert on every request (dropped by 011).

------------------------------------------------------------------ the rest
-- A name for a chat. Its own table because a title belongs to the
-- conversation, not to any one request in an append-only log. The gateway asks
-- a model for it with the caller's own request plus one message, on the
-- account that served the chat, because that reads the conversation's prompt
-- cache in full (measured 2026-09-17: cache_read=11406, where a title-only
-- system prompt wrote a fresh 7886-token entry instead).
CREATE TABLE chat_titles (
    conversation_id TEXT    PRIMARY KEY,
    title           TEXT    NOT NULL,
    -- Which model wrote it, and when: a title is generated text, and an odd one
    -- deserves to say what produced it.
    model           TEXT    NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    -- What it was written from: the chat's request count and the user's typed
    -- messages at the time. A title taken from the opening turn names how a
    -- chat started, which on production (2026-10-13) was routinely wrong: all
    -- twelve crush chats titled "Generate AGENTS.md" that week opened with
    -- crush's init prompt and ran 75 to 757 requests. A title written before
    -- the second message is replaced once. 0 when the gateway did not write it.
    requests        INTEGER NOT NULL DEFAULT 0,
    turns           INTEGER NOT NULL DEFAULT 0,
    -- An operator typed it, which holds it against every automatic rename.
    manual          INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;

-- The admin account. Exactly one, and the CHECK says so: this is an
-- operator's own gateway, and a second row would be a bug.
CREATE TABLE admin (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    password_hash TEXT    NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

-- Settings an operator changes while the gateway runs. config.yaml is read
-- once and never written back -- a process that rewrites its own config turns
-- a reviewable artefact into mutable state -- which is right for listen
-- addresses and wrong for turning an API surface off at three in the morning.
-- Key/value, because the next setting should not need a migration.
CREATE TABLE settings (
    key        TEXT    PRIMARY KEY,
    value      TEXT    NOT NULL,
    updated_at INTEGER NOT NULL
) WITHOUT ROWID;

-- Single-use sign-in links: a short random token rather than an encoded
-- credential, spent on first use. Stored rather than held in memory so one can
-- be minted from a shell, which is the case that matters -- a link made from a
-- live session is no help to someone who has none.
CREATE TABLE login_links (
    token      TEXT    PRIMARY KEY,
    expires_at INTEGER NOT NULL
) WITHOUT ROWID;
