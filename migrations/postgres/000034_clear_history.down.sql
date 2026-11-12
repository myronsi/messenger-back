ALTER TABLE chats DROP COLUMN IF EXISTS cleared_before_message_id;
ALTER TABLE participants DROP COLUMN IF EXISTS hidden_at;
ALTER TABLE participants DROP COLUMN IF EXISTS cleared_before_message_id;
