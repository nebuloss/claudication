-- What Anthropic tells us about each account's subscription quota.
--
-- Every relayed response carries it, in headers the gateway was already
-- forwarding and throwing away:
--
--   Anthropic-Ratelimit-Unified-5h-Utilization: 0.2
--   Anthropic-Ratelimit-Unified-5h-Reset:       1788803400
--   Anthropic-Ratelimit-Unified-5h-Status:      allowed
--   Anthropic-Ratelimit-Unified-7d-...
--
-- This is the number the client's own limit banner is built on, and for a pool
-- of accounts it is the whole game: it says which one has headroom left and
-- when the exhausted one comes back. Keeping it turns round-robin from "spread
-- evenly and hope" into a decision.
--
-- -1 means "never observed" and is deliberately not 0: an account we have not
-- heard from is unknown, not idle, and the two route differently.
ALTER TABLE accounts ADD COLUMN quota_updated_at TEXT NOT NULL DEFAULT '';
ALTER TABLE accounts ADD COLUMN quota_5h_util    REAL NOT NULL DEFAULT -1;
ALTER TABLE accounts ADD COLUMN quota_5h_reset   TEXT NOT NULL DEFAULT '';
ALTER TABLE accounts ADD COLUMN quota_5h_status  TEXT NOT NULL DEFAULT '';
ALTER TABLE accounts ADD COLUMN quota_7d_util    REAL NOT NULL DEFAULT -1;
ALTER TABLE accounts ADD COLUMN quota_7d_reset   TEXT NOT NULL DEFAULT '';
ALTER TABLE accounts ADD COLUMN quota_7d_status  TEXT NOT NULL DEFAULT '';
