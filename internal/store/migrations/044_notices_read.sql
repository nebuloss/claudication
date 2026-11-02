-- Version 43 to 44: notifications the operator has marked read.
--
-- A notification is worked out from the gateway's state on every read and
-- goes once its problem does; one that will not go — a model retired
-- upstream, failing everywhere for good — stayed in the bell's count with no
-- way to say it had been seen. The mark is kept by the notice's id until the
-- problem is gone.
CREATE TABLE notices_read (
    id      TEXT    PRIMARY KEY,
    read_at INTEGER NOT NULL
) WITHOUT ROWID;
