-- Classify each request's outcome once, when it is recorded.
--
-- The Message column can be filtered, and nothing else on the row could carry
-- that filter. The error text cannot: every upstream error ends in its own
-- request_id, so no two are equal and an exact match finds exactly one row.
-- Matching by LIKE instead would scan the table for every menu and still
-- double-count, because the classes overlap — a content refusal is also an
-- invalid_request_error, and a request that got no answer has an error too.
--
-- So the class is decided once, where it is already decided for display, and
-- stored. Exact, indexable, and one vocabulary shared by the column, the menu
-- and the filter rather than three that can disagree.
ALTER TABLE usage_events ADD COLUMN error_code TEXT NOT NULL DEFAULT '';

-- Filtering by one class, newest first, is the read this exists for.
CREATE INDEX IF NOT EXISTS idx_usage_error_code_at ON usage_events(error_code, at DESC);

-- Backfill, most specific first, so a row takes the narrowest class it fits.
--
-- Best effort by design: these patterns are read off errors this gateway has
-- actually recorded, and anything they miss lands in 'other' rather than
-- pretending to a precision the old rows cannot support. New rows are
-- classified at the source and do not come through here.

-- The refusal whose message is about billing and means nothing of the sort.
-- First, because it is also an invalid_request_error and the specific answer
-- is the useful one.
UPDATE usage_events SET error_code = 'content_check'
 WHERE error_code = '' AND error <> '' AND error LIKE '%extra usage%';

-- The upstream's own type, where it named one.
UPDATE usage_events SET error_code = 'authentication_error'
 WHERE error_code = '' AND error LIKE '%"type":"authentication_error"%';
UPDATE usage_events SET error_code = 'invalid_request_error'
 WHERE error_code = '' AND error LIKE '%"type":"invalid_request_error"%';
UPDATE usage_events SET error_code = 'rate_limit_error'
 WHERE error_code = '' AND error LIKE '%"type":"rate_limit_error"%';
UPDATE usage_events SET error_code = 'overloaded_error'
 WHERE error_code = '' AND error LIKE '%"type":"overloaded_error"%';
UPDATE usage_events SET error_code = 'permission_error'
 WHERE error_code = '' AND error LIKE '%"type":"permission_error"%';
UPDATE usage_events SET error_code = 'not_found_error'
 WHERE error_code = '' AND error LIKE '%"type":"not_found_error"%';
UPDATE usage_events SET error_code = 'request_too_large'
 WHERE error_code = '' AND error LIKE '%"type":"request_too_large"%';
UPDATE usage_events SET error_code = 'api_error'
 WHERE error_code = '' AND error LIKE '%"type":"api_error"%';

-- Nothing came back at all: the gateway recording that it never got a status.
UPDATE usage_events SET error_code = 'no_answer'
 WHERE error_code = '' AND error <> '' AND status = 0;

-- Everything else that failed in some way we cannot name from here.
UPDATE usage_events SET error_code = 'other'
 WHERE error_code = '' AND error <> '';
