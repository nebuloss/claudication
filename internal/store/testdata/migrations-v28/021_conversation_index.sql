-- A chat's requests are read by conversation, oldest to newest: the chat
-- detail, and the context each chat in the list is carrying now. Without this
-- both walked the whole table to find one conversation's rows.
CREATE INDEX IF NOT EXISTS idx_usage_conversation_at ON usage_events (conversation_id, at);
