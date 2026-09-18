-- Record requests that never reached the upstream, and keep them out of usage.
--
-- Until now this table held only relayed requests, because a request without a
-- valid key is refused in middleware and never reaches the recorder. That made
-- the most interesting rows invisible: someone pointing a client at the gateway
-- with no key, or the wrong one, produced nothing but a line in the log file.
--
-- One table rather than two, and certainly not a second database. A refused
-- request is the same shape as a relayed one with its key, model and tokens
-- missing, and the read path that matters — one time-ordered, keyset-paged,
-- filtered stream — is exactly what splitting them would break. Two tables
-- would mean interleaving two cursors by timestamp to page a single list, two
-- retention sweeps, and every filter written twice. A second database would add
-- two migration sets and a backup that has to stay consistent across both, to
-- isolate a write volume of a few thousand rows a week.
--
-- So: a column that says which kind a row is, and every aggregate filters on
-- it.
--
-- Named for the exception rather than the rule, so that zero means the common
-- case in both places. A `relayed` column defaulting to 1 would have a Go field
-- whose zero value is false, and every caller that forgot to set it would file
-- a relayed request as a refused one — silently, and in the direction that
-- removes rows from the usage figures. Naming it `rejected` makes the defaults
-- agree: the column defaults to 0, the field to false, and both mean "this was
-- relayed", which every row already in the table was.
ALTER TABLE usage_events ADD COLUMN rejected INTEGER NOT NULL DEFAULT 0;

-- Who it came from, which is the only identity a refused request has. A
-- relayed one is identified by its key; a rejected one has none, and without an
-- address there is nothing to tell one client's misconfiguration from a scan.
--
-- It is written for rejected requests only. A relayed request already carries a
-- key that names it, and recording an address beside it would collect more than
-- the question needs.
ALTER TABLE usage_events ADD COLUMN ip TEXT NOT NULL DEFAULT '';

-- The request log reads newest-first and filters; usage reads a window and
-- groups. This serves the first without making the second pay for it — the
-- existing idx_usage_at still covers the aggregates, which all narrow on
-- rejected = 0 now.
CREATE INDEX IF NOT EXISTS idx_usage_rejected_at ON usage_events (rejected, at DESC);
