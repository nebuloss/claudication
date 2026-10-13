-- What a chat title was written from, so the gateway can tell when it has gone
-- stale.
--
-- A title taken from a chat's opening turn names how it started, and on
-- production that was routinely the wrong thing (2026-10-13): of the twelve
-- crush chats titled some variant of "Generate AGENTS.md" in the week before,
-- all twelve opened with crush's init prompt — every one of their title
-- requests was exactly 655 input tokens — and they were the long ones, 75 to
-- 757 requests over several days. The name described the first five minutes.
--
-- requests is how many requests the conversation had when the title was
-- written; turns is how many messages the user had typed by then. Both are 0
-- for a title the gateway did not write itself (one read off a client's own
-- title request, or any row from before this migration), which is what makes
-- those eligible to be replaced once the chat has grown.
ALTER TABLE chat_titles ADD COLUMN requests INTEGER NOT NULL DEFAULT 0;
ALTER TABLE chat_titles ADD COLUMN turns INTEGER NOT NULL DEFAULT 0;
