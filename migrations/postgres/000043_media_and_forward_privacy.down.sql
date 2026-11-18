DELETE FROM user_privacy_exceptions WHERE setting_key IN ('voice_video_messages', 'forward_link');

DO $$
DECLARE
    keys TEXT[];
BEGIN
    SELECT array_agg(DISTINCT m[1]) INTO keys
    FROM pg_constraint c, regexp_matches(pg_get_constraintdef(c.oid), '''([a-z_]+)''', 'g') AS m
    WHERE c.conname = 'user_privacy_exceptions_setting_key_check'
      AND m[1] NOT IN ('voice_video_messages', 'forward_link');
    IF keys IS NULL THEN
        RAISE EXCEPTION 'user_privacy_exceptions_setting_key_check lists no setting keys';
    END IF;
    ALTER TABLE user_privacy_exceptions DROP CONSTRAINT user_privacy_exceptions_setting_key_check;
    EXECUTE format('ALTER TABLE user_privacy_exceptions ADD CONSTRAINT user_privacy_exceptions_setting_key_check CHECK (setting_key IN (%s))',
        (SELECT string_agg(quote_literal(k), ', ') FROM unnest(keys) AS k));
END
$$;

ALTER TABLE user_privacy_settings
    DROP COLUMN IF EXISTS voice_video_messages,
    DROP COLUMN IF EXISTS forward_link;
