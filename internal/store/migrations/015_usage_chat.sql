-- Which chat a request belonged to, and which client made it.
--
-- 006 recorded who spent the tokens (key, account) and on what (model, path),
-- which answers "what did today cost" but not "what did that conversation
-- cost" — and a conversation is the unit a person actually thinks in. One
-- runaway chat and one busy week look identical in a per-day rollup.
--
-- The id is the one the client already sends, never anything derived from the
-- messages. Measured on the wire, 2026-09-17: Claude Code sends
-- X-Claude-Code-Session-Id (and the same uuid inside metadata.user_id),
-- opencode and crush send x-session-id, Codex sends session_id. So every
-- client is covered by reading headers, and the gateway never has to look at
-- conversation content to group requests — see internal/api/conversation.go
-- for the table and how each was measured.
--
-- Empty when the client sent nothing. That is a real state, not a defect, and
-- it is left empty rather than filled with a guess: a fabricated grouping is
-- indistinguishable from a true one once it is in the table, and the empty
-- bucket is how an operator finds out a client needs configuring.
ALTER TABLE usage_events ADD COLUMN conversation_id TEXT NOT NULL DEFAULT '';

-- The product token from the User-Agent ("Claude Code", "opencode", "Codex").
--
-- Stored rather than derived on read because it cannot be recovered later: the
-- User-Agent is not kept, and x-session-id alone cannot tell opencode from
-- crush. It is also what makes the chat list legible — a bare uuid says
-- nothing, and the client that made it is most of what an operator needs to
-- find the conversation again in their own tooling.
ALTER TABLE usage_events ADD COLUMN client TEXT NOT NULL DEFAULT '';

-- Every per-chat query is "this chat's events" or "the chats active lately",
-- and both start from the same two columns. Partial, because the rows with no
-- conversation id are the one group nobody drills into and they would
-- otherwise be the largest entry in the index.
CREATE INDEX IF NOT EXISTS idx_usage_chat_at
    ON usage_events (conversation_id, at DESC)
    WHERE conversation_id <> '';
