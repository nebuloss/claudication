-- Explicit ordering for the account pool.
--
-- Which account serves a request is an operator's decision, not something to
-- infer: a personal subscription and a work one are not interchangeable, and
-- "spread the load evenly" is the wrong answer when one of them is the account
-- you would rather not spend. So accounts are a priority list, and the first
-- one with room left serves.
--
-- Quota still matters, but as a reason to skip rather than as the ordering:
-- the top account is used until Anthropic says it is out, then the next.
--
-- Existing rows are seeded by creation order, which is the order they have
-- effectively had until now.
ALTER TABLE accounts ADD COLUMN position INTEGER NOT NULL DEFAULT 0;

UPDATE accounts
   SET position = (SELECT COUNT(*) FROM accounts AS earlier
                    WHERE earlier.created_at < accounts.created_at
                       OR (earlier.created_at = accounts.created_at
                           AND earlier.id < accounts.id));

CREATE INDEX IF NOT EXISTS idx_accounts_position ON accounts (position);
