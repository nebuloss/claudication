-- The subscription usage the account reports for itself.
--
-- 007 read this off the rate-limit headers of relayed responses, which meant
-- an account showed nothing at all until it had served a request — and a
-- gateway you have just connected an account to has served none. Worse, the
-- headers carry only the two headline windows, while `/usage` in the client
-- shows the per-model weekly limits too.
--
-- So the figures now come from /api/oauth/usage, the endpoint the client's own
-- fetchUtilization calls, polled on a schedule instead of arriving as a side
-- effect of traffic. usage_json holds the normalised `limits` array verbatim so
-- the UI can render exactly what `/usage` prints; the two columns beside it are
-- the headline percentages, kept separate so the pool can order on them without
-- parsing JSON per request.
ALTER TABLE accounts ADD COLUMN usage_json TEXT NOT NULL DEFAULT '';

-- The 007 columns are reused rather than replaced: same meaning, better
-- source. They are cleared here because the old values were 0-1 fractions
-- read from headers and the new ones are 0-100 percentages from the endpoint,
-- and a mixed column would render one account's 43% as 4300%.
UPDATE accounts
   SET quota_5h_util = -1, quota_7d_util = -1,
       quota_5h_reset = '', quota_7d_reset = '',
       quota_5h_status = '', quota_7d_status = '',
       quota_updated_at = '';
