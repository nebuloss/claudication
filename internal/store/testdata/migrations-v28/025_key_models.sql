-- Which models an API key may ask for.
--
-- The same shape as account_models_off (022) and for the same reason: kept as
-- what is off, so a model Anthropic releases tomorrow is usable by every key
-- until someone decides otherwise, and a key handed to a script can be kept
-- off the expensive model without listing every cheap one.
--
-- Matched to requests by store.ModelKey, so a dated id and its alias are one
-- model and turning one off covers both.
CREATE TABLE IF NOT EXISTS api_key_models_off (
    key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    model  TEXT NOT NULL,
    off_at TEXT NOT NULL,
    PRIMARY KEY (key_id, model)
);
