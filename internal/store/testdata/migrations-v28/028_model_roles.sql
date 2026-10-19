-- Model roles replace the free-form virtual models of 027.
--
-- 027 let an operator invent names, give each an ordered list of fallbacks,
-- and scope it to a key or to one client app on a key. It worked and was too
-- much to use. What the clients need is narrower, measured by reading each
-- one (2026-10-15): every one has a slot for the model doing the work and a
-- slot for titles and summaries — crush large and small, opencode model and
-- small_model, Codex model, Claude Code's opus and haiku tiers. So there are
-- two roles, claude-large and claude-small, and every config the gateway
-- writes uses them.
--
-- A role is bound to one real model. The gateway-wide binding (key_id empty)
-- with model empty means automatic: the newest Opus for large, the newest
-- Haiku for small. A key's row overrides the gateway's for that key; no row
-- means the key follows the gateway.
--
-- usage_events.virtual_model from 027 stays: it now records the role a
-- request asked for.
DROP TABLE IF EXISTS virtual_models;

CREATE TABLE IF NOT EXISTS model_roles (
    key_id     TEXT NOT NULL DEFAULT '',
    role       TEXT NOT NULL,
    model      TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL,
    PRIMARY KEY (key_id, role)
) WITHOUT ROWID;
