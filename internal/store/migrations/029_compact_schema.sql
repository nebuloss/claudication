-- Version 28 to 29: the whole schema rebuilt as schema.sql has it.
--
-- What changes, and why each is in schema.sql beside the table it concerns:
-- every moment becomes INTEGER unix ms; usage_events trades `rejected` and the
-- '/internal/title' path test for `kind`, and key_name/account_email for the
-- names table; the quota moves to account_quota, with absence and NULL for the
-- old -1 and ''; model_roles' gateway rows get key_id NULL and a foreign key;
-- rpm_limit, virtual_model and usage_json become rate_limit, alias and detail;
-- AUTOINCREMENT and the indexes nothing needed go.
--
-- Built by setting every old table aside under legacy_*, creating the new ones
-- from schema.sql itself (the include below), copying across, and dropping the
-- legacy tables. Creating from schema.sql rather than from a copy of it here
-- means an upgraded database and a fresh one cannot differ by a typo; it also
-- means this step is only right while schema.sql is at version 29. Whoever adds
-- step 030 replaces the include with the version-29 text of schema.sql.
--
-- Runs inside migrate()'s transaction with foreign_keys=ON, which cannot be
-- turned off inside one. That decides the order: renaming a parent re-points
-- its children's REFERENCES at the new name, so the legacy children follow
-- their legacy parents; the copy fills parents before children; and the legacy
-- children are dropped before their parents, so no DROP cascades into a row
-- still wanted.

------------------------------------------------------------------ set aside
-- Index names are global, and idx_usage_at is reused. sqlite_autoindex_*
-- follow their table's rename by themselves.
DROP INDEX IF EXISTS idx_api_keys_prefix;
DROP INDEX IF EXISTS idx_accounts_provider;
DROP INDEX IF EXISTS idx_accounts_position;
DROP INDEX IF EXISTS idx_login_links_expires;
DROP INDEX IF EXISTS idx_usage_at;
DROP INDEX IF EXISTS idx_usage_chat_at;
DROP INDEX IF EXISTS idx_usage_conversation_at;
DROP INDEX IF EXISTS idx_usage_error_code_at;
DROP INDEX IF EXISTS idx_usage_rejected_at;

ALTER TABLE schema_migrations  RENAME TO legacy_schema_migrations;
ALTER TABLE api_keys           RENAME TO legacy_api_keys;
ALTER TABLE api_key_models_off RENAME TO legacy_api_key_models_off;
ALTER TABLE model_roles        RENAME TO legacy_model_roles;
ALTER TABLE accounts           RENAME TO legacy_accounts;
ALTER TABLE account_models_off RENAME TO legacy_account_models_off;
ALTER TABLE usage_events       RENAME TO legacy_usage_events;
ALTER TABLE chat_titles        RENAME TO legacy_chat_titles;
ALTER TABLE admin              RENAME TO legacy_admin;
ALTER TABLE settings           RENAME TO legacy_settings;
ALTER TABLE login_links        RENAME TO legacy_login_links;

-- include schema.sql

------------------------------------------------------------------ copy
-- An RFC3339 text time, with or without a fraction, always ending in Z (every
-- value was written in UTC), to unix ms, written out inline because SQL has
-- no functions of its own here:
--
--   CAST(strftime('%s', substr(x,1,19)) AS INTEGER) * 1000
--   + CASE WHEN substr(x,20,1) = '.'
--          THEN CAST(substr(rtrim(substr(x,21),'Z') || '000', 1, 3) AS INTEGER)
--          ELSE 0 END
--
-- Truncated to the millisecond, as Go's UnixMilli truncates, so an old
-- request-log cursor lands on the row it named. '' and NULL become NULL, which
-- the NOT NULL columns COALESCE to 0. The columns written with
-- time.RFC3339 never had a fraction and take the first line alone.

INSERT INTO api_keys (id, name, prefix, hash, sealed_secret, created_at, last_used_at,
                      rate_limit, rate_period_s, token_budget)
SELECT id, name, prefix, hash, sealed_secret,
       COALESCE(CAST(strftime('%s', substr(created_at,1,19)) AS INTEGER) * 1000, 0),
       CAST(strftime('%s', substr(NULLIF(last_used_at,''),1,19)) AS INTEGER) * 1000,
       MAX(rpm_limit, 0),
       CASE WHEN rate_period_s > 0 THEN rate_period_s ELSE 60 END,
       MAX(token_budget, 0)
  FROM legacy_api_keys;

INSERT INTO api_key_models_off (key_id, model, off_at)
SELECT key_id, model, COALESCE(CAST(strftime('%s', substr(off_at,1,19)) AS INTEGER) * 1000, 0)
  FROM legacy_api_key_models_off;

-- The gateway's rows were key_id ''. A key's rows outlived it when DeleteKey
-- failed between its two statements; those are dropped, as the foreign key
-- would refuse them.
INSERT INTO model_roles (key_id, role, model, updated_at)
SELECT NULLIF(key_id, ''), role, model,
       COALESCE(CAST(strftime('%s', substr(updated_at,1,19)) AS INTEGER) * 1000
       + CASE WHEN substr(updated_at,20,1) = '.'
              THEN CAST(substr(rtrim(substr(updated_at,21),'Z') || '000', 1, 3) AS INTEGER) ELSE 0 END, 0)
  FROM legacy_model_roles
 WHERE key_id = '' OR key_id IN (SELECT id FROM legacy_api_keys);

INSERT INTO accounts (id, provider, email, account_uuid, access_token, refresh_token,
                      expires_at, refresh_expires_at, created_at, last_refresh_at, last_used_at,
                      last_error, disabled_at, refresh_dead_at, position)
SELECT id, provider, email, account_uuid, access_token, refresh_token,
       COALESCE(CAST(strftime('%s', substr(expires_at,1,19)) AS INTEGER) * 1000, 0),
       CAST(strftime('%s', substr(NULLIF(refresh_expires_at,''),1,19)) AS INTEGER) * 1000,
       COALESCE(CAST(strftime('%s', substr(created_at,1,19)) AS INTEGER) * 1000, 0),
       CAST(strftime('%s', substr(NULLIF(last_refresh_at,''),1,19)) AS INTEGER) * 1000,
       CAST(strftime('%s', substr(NULLIF(last_used_at,''),1,19)) AS INTEGER) * 1000,
       COALESCE(last_error, ''),
       CAST(strftime('%s', substr(NULLIF(disabled_at,''),1,19)) AS INTEGER) * 1000,
       CAST(strftime('%s', substr(NULLIF(refresh_dead_at,''),1,19)) AS INTEGER) * 1000,
       position
  FROM legacy_accounts;

-- quota_updated_at '' was never fetched: no row. -1 was a window not reported:
-- NULL.
INSERT INTO account_quota (account_id, updated_at,
                           five_hour_util, five_hour_reset, five_hour_status,
                           seven_day_util, seven_day_reset, seven_day_status, detail)
SELECT id,
       COALESCE(CAST(strftime('%s', substr(quota_updated_at,1,19)) AS INTEGER) * 1000, 0),
       CASE WHEN quota_5h_util < 0 THEN NULL ELSE quota_5h_util END,
       CAST(strftime('%s', substr(NULLIF(quota_5h_reset,''),1,19)) AS INTEGER) * 1000,
       quota_5h_status,
       CASE WHEN quota_7d_util < 0 THEN NULL ELSE quota_7d_util END,
       CAST(strftime('%s', substr(NULLIF(quota_7d_reset,''),1,19)) AS INTEGER) * 1000,
       quota_7d_status,
       usage_json
  FROM legacy_accounts
 WHERE quota_updated_at <> '';

INSERT INTO account_models_off (account_id, model, off_at)
SELECT account_id, model, COALESCE(CAST(strftime('%s', substr(off_at,1,19)) AS INTEGER) * 1000, 0)
  FROM legacy_account_models_off;

-- The names of what exists, then, for ids already deleted, the name on their
-- newest event. SQLite takes a bare column from the row MAX() picked.
INSERT INTO names (kind, id, name) SELECT 'key', id, name FROM legacy_api_keys;
INSERT INTO names (kind, id, name) SELECT 'account', id, email FROM legacy_accounts;
INSERT OR IGNORE INTO names (kind, id, name)
SELECT 'key', key_id, key_name FROM
  (SELECT key_id, key_name, MAX(id) FROM legacy_usage_events
    WHERE key_id <> '' AND key_name <> '' GROUP BY key_id);
INSERT OR IGNORE INTO names (kind, id, name)
SELECT 'account', account_id, account_email FROM
  (SELECT account_id, account_email, MAX(id) FROM legacy_usage_events
    WHERE account_id <> '' AND account_email <> '' GROUP BY account_id);

-- error_code gains the failures it did not name: a status of 0 or >= 400 with
-- no text, which the classifier, reading only text, left empty.
INSERT INTO usage_events (id, at, kind, key_id, account_id, model, alias, path,
                          conversation_id, client, ip, status, streaming,
                          input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
                          duration_ms, first_token_ms, error_code, error)
SELECT id,
       COALESCE(CAST(strftime('%s', substr(at,1,19)) AS INTEGER) * 1000
       + CASE WHEN substr(at,20,1) = '.'
              THEN CAST(substr(rtrim(substr(at,21),'Z') || '000', 1, 3) AS INTEGER) ELSE 0 END, 0),
       CASE WHEN rejected <> 0 THEN 1 WHEN path = '/internal/title' THEN 2 ELSE 0 END,
       key_id, account_id, model, virtual_model, path, conversation_id, client, ip,
       status, streaming, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
       duration_ms, first_token_ms,
       CASE WHEN error_code <> '' THEN error_code
            WHEN status = 0 AND error = '' THEN 'no_answer'
            WHEN status = 0 OR status >= 400 OR error <> '' THEN 'other'
            ELSE '' END,
       error
  FROM legacy_usage_events
 ORDER BY id;

INSERT INTO chat_titles (conversation_id, title, model, created_at, requests, turns, manual)
SELECT conversation_id, title, model,
       COALESCE(CAST(strftime('%s', substr(created_at,1,19)) AS INTEGER) * 1000
       + CASE WHEN substr(created_at,20,1) = '.'
              THEN CAST(substr(rtrim(substr(created_at,21),'Z') || '000', 1, 3) AS INTEGER) ELSE 0 END, 0),
       requests, turns, manual
  FROM legacy_chat_titles;

INSERT INTO admin (id, password_hash, created_at, updated_at)
SELECT id, password_hash,
       COALESCE(CAST(strftime('%s', substr(created_at,1,19)) AS INTEGER) * 1000, 0),
       COALESCE(CAST(strftime('%s', substr(updated_at,1,19)) AS INTEGER) * 1000, 0)
  FROM legacy_admin;

-- Was unix seconds.
INSERT INTO settings (key, value, updated_at)
SELECT key, value, updated_at * 1000 FROM legacy_settings;

-- Only the links still worth spending.
INSERT INTO login_links (token, expires_at)
SELECT token,
       CAST(strftime('%s', substr(expires_at,1,19)) AS INTEGER) * 1000
       + CASE WHEN substr(expires_at,20,1) = '.'
              THEN CAST(substr(rtrim(substr(expires_at,21),'Z') || '000', 1, 3) AS INTEGER) ELSE 0 END
  FROM legacy_login_links
 WHERE CAST(strftime('%s', substr(expires_at,1,19)) AS INTEGER) > unixepoch();

------------------------------------------------------------------ drop
-- Children before parents. The history of 001-028 goes with
-- legacy_schema_migrations: the files are gone, and migrate() records 29.
DROP TABLE legacy_api_key_models_off;
DROP TABLE legacy_account_models_off;
DROP TABLE legacy_model_roles;
DROP TABLE legacy_api_keys;
DROP TABLE legacy_accounts;
DROP TABLE legacy_usage_events;
DROP TABLE legacy_chat_titles;
DROP TABLE legacy_admin;
DROP TABLE legacy_settings;
DROP TABLE legacy_login_links;
DROP TABLE legacy_schema_migrations;
