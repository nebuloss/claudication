-- Version 35 to 36: the gateway records its own stops, to the second.
--
-- A new table and nothing else. The CREATE statement below is schema.sql's at
-- version 36, copied in; do not edit it, it is what this step made.

CREATE TABLE uptime_outages (
    -- When the gateway stopped taking requests, unix milliseconds UTC.
    started INTEGER PRIMARY KEY,
    -- When it took them again; NULL while it has not.
    ended   INTEGER
) WITHOUT ROWID;
