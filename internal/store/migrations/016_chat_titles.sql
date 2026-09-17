-- A name for a chat, when one can be had.
--
-- Its own table rather than a column on usage_events, because a title belongs
-- to the conversation and not to any one request: usage_events is an
-- append-only log of things that happened, and writing a title into it would
-- mean either updating rows that are meant to be immutable or repeating the
-- same string on every turn.
--
-- Where the title comes from: the gateway asks for it, once, the first time it
-- sees a conversation. Not from parsing the messages — the only text in a
-- request that reads like a title is the conversation itself, and the working
-- directory that would do instead sits inside the message content rather than
-- a header, behind an undocumented format that moves with every client
-- release.
--
-- Measured before building it (2026-09-17), because the cost is the whole
-- question. A side request that keeps the caller's system prompt and tools and
-- appends its instruction as a trailing user message reads the conversation's
-- existing prompt cache in full:
--
--     main conversation, warming        cache_create=11406  cache_read=    0
--     side call, own system prompt      cache_create= 7886  cache_read=    0
--     side call, caller's prompt+tools  cache_create=    0  cache_read=11406
--
-- The obvious shape — swapping in a "you are a title generator" system
-- prompt — misses the cache completely and writes a second entry on top of it.
-- So the title request is the caller's own request with one message appended,
-- and it is made on the account that served the chat, since the cache is
-- per-account.
CREATE TABLE IF NOT EXISTS chat_titles (
    conversation_id TEXT PRIMARY KEY,
    title           TEXT NOT NULL,
    -- Which model wrote it, and when. Kept because a title is generated text
    -- and an operator looking at an odd one deserves to know what produced it.
    model      TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
) WITHOUT ROWID;
