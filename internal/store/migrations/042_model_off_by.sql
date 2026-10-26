-- Version 41 to 42: a model switched off on an account says who switched it
-- off.
--
-- The gateway now switches a model off itself when it has failed on the
-- account for a while for a reason that lasts, and back on when a check finds
-- it served again (internal/service/availability). It must never turn back on
-- a model the operator switched off, so each row says whose it is: 'operator'
-- or 'auto'. Every row so far is the operator's.
--
-- Rebuilt, not altered, so the table reads as schema.sql writes it.
ALTER TABLE account_models_off RENAME TO legacy_account_models_off;

CREATE TABLE account_models_off (
    account_id TEXT    NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    model      TEXT    NOT NULL,
    off_at     INTEGER NOT NULL,
    by         TEXT    NOT NULL DEFAULT 'operator',
    PRIMARY KEY (account_id, model)
) WITHOUT ROWID;

INSERT INTO account_models_off (account_id, model, off_at, by)
SELECT account_id, model, off_at, 'operator' FROM legacy_account_models_off;

DROP TABLE legacy_account_models_off;
