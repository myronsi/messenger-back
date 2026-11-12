ALTER TABLE participants ADD COLUMN archived_at TIMESTAMPTZ;

ALTER TABLE user_chat_pins ADD COLUMN position INT NOT NULL DEFAULT 0;
UPDATE user_chat_pins pin
SET position = ranked.position
FROM (
    SELECT user_id, chat_id, (row_number() OVER (PARTITION BY user_id ORDER BY pinned_at DESC, chat_id DESC) - 1)::int AS position
    FROM user_chat_pins
) ranked
WHERE pin.user_id = ranked.user_id AND pin.chat_id = ranked.chat_id;

ALTER TABLE chats DROP CONSTRAINT chats_direct_key_format;
ALTER TABLE chats ADD CONSTRAINT chats_direct_key_format CHECK (
    direct_key IS NULL
    OR (direct_key ~ '^[1-9][0-9]{0,17}:[1-9][0-9]{0,17}$'
        AND split_part(direct_key, ':', 1)::bigint <= split_part(direct_key, ':', 2)::bigint)
);
