-- Virtual models: names clients are configured with, mapped here to the real
-- models that serve them.
--
-- So that changing which model a client uses is an edit in the gateway, not a
-- round of every client's configuration: a client asks for claude-smart, and
-- the operator decides today that claude-smart is Opus 5.5. targets is an
-- ordered list, \x1f-joined like the model switches: the first one an account
-- can serve and the asking key may use is the one that answers.
--
-- Scoped, most specific first: a mapping for one key and one client app
-- (Claude Code, Codex, opencode, crush — as the request log names them), then
-- for one key and any app, then the gateway-wide one with both empty. So the
-- laptop key's Codex can be on one model while its Claude Code is on another,
-- and every other key keeps the gateway's default.
--
-- key_id is not a foreign key because the gateway-wide rows have none;
-- DeleteKey removes a key's rows itself.
CREATE TABLE IF NOT EXISTS virtual_models (
    key_id       TEXT NOT NULL DEFAULT '',
    client       TEXT NOT NULL DEFAULT '',
    name         TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    targets      TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (key_id, client, name)
) WITHOUT ROWID;

-- What a request asked for when that was a virtual model, beside the real
-- model it was served by, which stays in model: model is what was spent and
-- what every report groups by, this is what the client was configured with.
ALTER TABLE usage_events ADD COLUMN virtual_model TEXT NOT NULL DEFAULT '';
