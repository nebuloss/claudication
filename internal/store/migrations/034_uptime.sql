-- Version 33 to 34: the gateway records its own availability.
--
-- A new table and nothing else. The CREATE statement below is schema.sql's at
-- version 34, copied in; do not edit it, it is what this step made.

------------------------------------------------------------------ uptime
CREATE TABLE uptime_samples (
    -- The minute's start, unix milliseconds UTC.
    minute INTEGER PRIMARY KEY,
    -- 1 up, 2 paused (the gateway switch off), 3 degraded (accepting, but no
    -- account able to serve). The worst seen in the minute wins.
    state  INTEGER NOT NULL
) WITHOUT ROWID;
