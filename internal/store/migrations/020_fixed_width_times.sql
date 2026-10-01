-- Rewrite the times that are compared as text into one fixed width.
--
-- These columns were written with Go's RFC3339Nano, which drops trailing
-- zeros: "10:00:00.5Z" and "10:00:00Z" are different lengths, and as text the
-- first sorts before the second although it is half a second later. Every
-- window in the gateway — budgets, reports, the request log's paging, the
-- retention prune, sign-in link expiry — compares these columns as text, so
-- a window starting on a whole second could drop or include the events of
-- that second wrongly. New rows are written with nine fraction digits always
-- (store.stamp); this brings the existing ones to the same shape, so old and
-- new compare correctly with each other.
--
-- Every value was written in UTC, so it ends in Z: "YYYY-MM-DDTHH:MM:SS",
-- then optionally "." and 1-9 digits, then "Z". The 19-character prefix is
-- kept, the fraction padded to nine digits, and Z appended. A value already
-- 30 characters long is already in shape and left alone.

UPDATE usage_events SET at =
    substr(at, 1, 19) || '.' ||
    substr(CASE WHEN substr(at, 20, 1) = '.' THEN substr(at, 21, length(at) - 21) ELSE '' END
           || '000000000', 1, 9) || 'Z'
 WHERE length(at) <> 30 AND at LIKE '____-__-__T__:__:__%Z';

UPDATE login_links SET
    created_at = substr(created_at, 1, 19) || '.' ||
        substr(CASE WHEN substr(created_at, 20, 1) = '.' THEN substr(created_at, 21, length(created_at) - 21) ELSE '' END
               || '000000000', 1, 9) || 'Z',
    expires_at = substr(expires_at, 1, 19) || '.' ||
        substr(CASE WHEN substr(expires_at, 20, 1) = '.' THEN substr(expires_at, 21, length(expires_at) - 21) ELSE '' END
               || '000000000', 1, 9) || 'Z'
 WHERE (length(created_at) <> 30 AND created_at LIKE '____-__-__T__:__:__%Z')
    OR (length(expires_at) <> 30 AND expires_at LIKE '____-__-__T__:__:__%Z');

UPDATE chat_titles SET created_at =
    substr(created_at, 1, 19) || '.' ||
    substr(CASE WHEN substr(created_at, 20, 1) = '.' THEN substr(created_at, 21, length(created_at) - 21) ELSE '' END
           || '000000000', 1, 9) || 'Z'
 WHERE length(created_at) <> 30 AND created_at LIKE '____-__-__T__:__:__%Z';
