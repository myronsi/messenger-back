DELETE FROM chats WHERE direct_key IS NOT NULL AND split_part(direct_key, ':', 1) = split_part(direct_key, ':', 2);
ALTER TABLE chats DROP CONSTRAINT chats_direct_key_format;
ALTER TABLE chats ADD CONSTRAINT chats_direct_key_format CHECK (
    direct_key IS NULL
    OR (direct_key ~ '^[1-9][0-9]{0,17}:[1-9][0-9]{0,17}$'
        AND split_part(direct_key, ':', 1)::bigint < split_part(direct_key, ':', 2)::bigint)
);

ALTER TABLE user_chat_pins DROP COLUMN IF EXISTS position;
ALTER TABLE participants DROP COLUMN IF EXISTS archived_at;
