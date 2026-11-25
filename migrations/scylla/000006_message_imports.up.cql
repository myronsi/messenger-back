ALTER TABLE messages ADD import_id bigint;
ALTER TABLE messages ADD import_sender text;
ALTER TABLE messages ADD original_at timestamp;
