CREATE OR REPLACE FUNCTION bump_profile_version() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.username IS DISTINCT FROM OLD.username
        OR NEW.display_name IS DISTINCT FROM OLD.display_name
        OR NEW.bio IS DISTINCT FROM OLD.bio
        OR NEW.avatar_url IS DISTINCT FROM OLD.avatar_url
        OR NEW.avatar_attachment_id IS DISTINCT FROM OLD.avatar_attachment_id THEN
        NEW.profile_version = OLD.profile_version + 1;
    END IF;
    RETURN NEW;
END;
$$;

DROP TABLE IF EXISTS birthday_reminders_sent;

ALTER TABLE notification_preferences DROP COLUMN IF EXISTS birthday_reminders;

DELETE FROM user_privacy_exceptions WHERE setting_key = 'birthday_visibility';
ALTER TABLE user_privacy_exceptions DROP CONSTRAINT user_privacy_exceptions_setting_key_check;
ALTER TABLE user_privacy_exceptions ADD CONSTRAINT user_privacy_exceptions_setting_key_check
    CHECK (setting_key IN ('avatar_visibility', 'profile_visibility', 'presence_visibility', 'last_seen_visibility',
        'group_invites', 'direct_messages', 'search_visibility'));
ALTER TABLE user_privacy_settings DROP COLUMN IF EXISTS birthday_visibility;

ALTER TABLE chats
    DROP COLUMN IF EXISTS background_kind,
    DROP COLUMN IF EXISTS background_attachment_id,
    DROP COLUMN IF EXISTS background_preset,
    DROP COLUMN IF EXISTS background_colors;

ALTER TABLE users
    DROP COLUMN IF EXISTS pronouns,
    DROP COLUMN IF EXISTS birthday_day,
    DROP COLUMN IF EXISTS birthday_month,
    DROP COLUMN IF EXISTS birthday_year,
    DROP COLUMN IF EXISTS time_zone,
    DROP COLUMN IF EXISTS music_attachment_id,
    DROP COLUMN IF EXISTS music_title,
    DROP COLUMN IF EXISTS music_artist,
    DROP COLUMN IF EXISTS background_kind,
    DROP COLUMN IF EXISTS background_attachment_id,
    DROP COLUMN IF EXISTS background_preset,
    DROP COLUMN IF EXISTS background_colors;

DELETE FROM attachments WHERE purpose IN ('profile_music', 'profile_background');
ALTER TABLE attachments DROP CONSTRAINT attachments_purpose_check;
ALTER TABLE attachments ADD CONSTRAINT attachments_purpose_check CHECK (purpose IN ('message', 'avatar', 'sticker'));
