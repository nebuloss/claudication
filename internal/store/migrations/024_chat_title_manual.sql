-- A title an operator typed.
--
-- Kept apart from the generated ones because the gateway now renames chats
-- as they grow (023), and a rename that overwrote a name someone chose would
-- undo the one title known to be right. manual = 1 holds a title against every
-- automatic rename until the operator asks for one again.
ALTER TABLE chat_titles ADD COLUMN manual INTEGER NOT NULL DEFAULT 0;
