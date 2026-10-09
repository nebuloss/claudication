-- The models an operator has turned off for an account. A row is an "off";
-- no row is on, so a model released after the switch was last touched is on
-- until someone says otherwise. Gone with its account.
CREATE TABLE IF NOT EXISTS account_models_off (
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    -- As the operator switched it, which is the upstream's listed id; matched
    -- to requests by store.ModelKey, so a dated id and its alias are one model.
    model      TEXT NOT NULL,
    off_at     TEXT NOT NULL,
    PRIMARY KEY (account_id, model)
);
