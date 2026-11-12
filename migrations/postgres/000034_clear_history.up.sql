ALTER TABLE participants ADD COLUMN cleared_before_message_id BIGINT;
ALTER TABLE participants ADD COLUMN hidden_at TIMESTAMPTZ;
ALTER TABLE chats ADD COLUMN cleared_before_message_id BIGINT;
