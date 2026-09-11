-- Runtime settings an operator changes while the gateway is running.
--
-- Everything in config.yaml is read once at startup and never written back, on
-- purpose: the file belongs to whoever deploys the machine, and a process that
-- rewrites its own config turns a reviewable artefact into mutable state. That
-- rule is right for listen addresses and state directories, and wrong for the
-- one thing an operator needs at three in the morning — turning an API surface
-- off. Editing a file, restarting, and dropping every in-flight stream is not
-- an answer to "stop serving this now".
--
-- So this table holds the settings that are genuinely runtime, and Settings()
-- reports them with origin "database" so the config screen still shows one
-- ordered story about where every value came from.
--
-- Deliberately a key/value table rather than a column per setting. There are
-- two rows today (one per API surface) and the next one will be added by code
-- that has no business writing a migration for a boolean.
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
