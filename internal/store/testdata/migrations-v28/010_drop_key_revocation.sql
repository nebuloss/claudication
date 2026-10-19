-- Revoking a key and deleting it were the same act wearing two names.
--
-- Revocation was one-way — there was never an un-revoke — so it was a delete
-- that left a row behind. The reason given for keeping the row was that usage
-- history should still resolve to a name, but usage_events already carries its
-- own copy of key_name for exactly that purpose, and does so precisely so the
-- history survives the key being deleted. Two buttons, one meaning, and the
-- one the operator reached for depended on which explanation they had read.
--
-- Deleting the revoked rows rather than only dropping the column is the whole
-- point. "revoked" meant "must not authenticate"; dropping the column while
-- keeping the rows would make every previously revoked key work again, which
-- is the exact opposite of what the operator asked for when they revoked it.
DELETE FROM api_keys WHERE revoked_at IS NOT NULL;

ALTER TABLE api_keys DROP COLUMN revoked_at;
