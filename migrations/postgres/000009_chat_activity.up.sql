-- Schema v2, part 9: what the chat list needs without asking the message store for every chat.

-- The newest message of a chat (a Snowflake id) and when the chat last changed, written on every send:
-- the chat list is ordered by it.
ALTER TABLE chats ADD COLUMN last_message_id BIGINT CHECK (last_message_id > 0);
ALTER TABLE chats ADD COLUMN last_activity_at TIMESTAMPTZ;
UPDATE chats SET last_activity_at = created_at WHERE last_activity_at IS NULL;
ALTER TABLE chats ALTER COLUMN last_activity_at SET NOT NULL;
ALTER TABLE chats ALTER COLUMN last_activity_at SET DEFAULT now();

-- When the read marker last moved: the read_at of read receipts.
ALTER TABLE participants ADD COLUMN last_read_at TIMESTAMPTZ;
