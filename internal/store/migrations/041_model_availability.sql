-- Version 40 to 41: a model's last result on an account says where it came
-- from, and when the model last worked there.
--
-- The row was the operator's last Test. Now the gateway keeps it current
-- itself (internal/service/availability): it tests every model of an account
-- when the account is added, tests a failing one again every quarter of an
-- hour, and reads client traffic — a request refused for its model makes it
-- fail, one served makes it work. So the row says which of those it was
-- (source), and when the model last worked (ok_at), which a later failure
-- does not erase.
--
-- Rebuilt, not altered, so the table reads as schema.sql writes it.
ALTER TABLE account_model_tests RENAME TO legacy_account_model_tests;

CREATE TABLE account_model_tests (
    account_id TEXT    NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    model      TEXT    NOT NULL,
    at         INTEGER NOT NULL,
    ok         INTEGER NOT NULL,
    status     INTEGER NOT NULL DEFAULT 0,
    latency_ms INTEGER NOT NULL DEFAULT 0,
    error      TEXT    NOT NULL DEFAULT '',
    source     TEXT    NOT NULL DEFAULT 'test',
    ok_at      INTEGER,
    PRIMARY KEY (account_id, model)
) WITHOUT ROWID;

INSERT INTO account_model_tests (account_id, model, at, ok, status, latency_ms, error, source, ok_at)
SELECT account_id, model, at, ok, status, latency_ms, error, 'test', CASE WHEN ok THEN at END
  FROM legacy_account_model_tests;

DROP TABLE legacy_account_model_tests;
