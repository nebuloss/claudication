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

-- Which machine made the request.
--
-- For a refused one it is the only identity there is: no key, no client, and
-- without an address nothing to tell one misconfigured client from a scan.
--
-- Recorded for relayed requests too. A key says who is paying and the
-- User-Agent says what they are running, and neither says where it ran — so
-- when one host out of several is burning a subscription, this is the only
-- column that answers it.
--
-- It is only as true as the proxy in front makes it: without the fronting
-- address in trusted-proxies, X-Forwarded-For is ignored and every client is
-- recorded as the proxy. See config.trusted-proxies.
ALTER TABLE usage_events ADD COLUMN ip TEXT NOT NULL DEFAULT '';

-- The request log reads newest-first and filters; usage reads a window and
-- groups. This serves the first without making the second pay for it — the
-- existing idx_usage_at still covers the aggregates, which all narrow on
-- rejected = 0 now.
CREATE INDEX IF NOT EXISTS idx_usage_rejected_at ON usage_events (rejected, at DESC);
