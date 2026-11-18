ALTER TABLE user_privacy_settings
    ADD COLUMN voice_video_messages TEXT NOT NULL DEFAULT 'everyone'
        CONSTRAINT user_privacy_settings_voice_video_messages_check CHECK (voice_video_messages IN ('everyone', 'shared_chats')),
    ADD COLUMN forward_link TEXT NOT NULL DEFAULT 'everyone'
        CONSTRAINT user_privacy_settings_forward_link_check CHECK (forward_link IN ('everyone', 'shared_chats', 'nobody'));

DO $$
DECLARE
    keys TEXT[];
BEGIN
    SELECT array_agg(DISTINCT m[1]) INTO keys
    FROM pg_constraint c, regexp_matches(pg_get_constraintdef(c.oid), '''([a-z_]+)''', 'g') AS m
    WHERE c.conname = 'user_privacy_exceptions_setting_key_check';
    IF keys IS NULL THEN
        RAISE EXCEPTION 'user_privacy_exceptions_setting_key_check lists no setting keys';
    END IF;
    keys := array_cat(keys, ARRAY['voice_video_messages', 'forward_link']);
    ALTER TABLE user_privacy_exceptions DROP CONSTRAINT user_privacy_exceptions_setting_key_check;
    EXECUTE format('ALTER TABLE user_privacy_exceptions ADD CONSTRAINT user_privacy_exceptions_setting_key_check CHECK (setting_key IN (%s))',
        (SELECT string_agg(quote_literal(k), ', ') FROM unnest(keys) AS k));
END
$$;
