ALTER TABLE attachments DROP CONSTRAINT attachments_purpose_check;
ALTER TABLE attachments ADD CONSTRAINT attachments_purpose_check
    CHECK (purpose IN ('message', 'avatar', 'sticker', 'profile_music', 'profile_background'));

ALTER TABLE users
    ADD COLUMN pronouns TEXT CONSTRAINT users_pronouns_length CHECK (char_length(pronouns) BETWEEN 1 AND 40),
    ADD COLUMN birthday_day SMALLINT,
    ADD COLUMN birthday_month SMALLINT,
    ADD COLUMN birthday_year SMALLINT,
    ADD COLUMN time_zone TEXT NOT NULL DEFAULT 'UTC' CONSTRAINT users_time_zone_length CHECK (char_length(time_zone) BETWEEN 1 AND 64),
    ADD COLUMN music_attachment_id UUID REFERENCES attachments (id) ON DELETE SET NULL,
    ADD COLUMN music_title TEXT CONSTRAINT users_music_title_length CHECK (char_length(music_title) BETWEEN 1 AND 100),
    ADD COLUMN music_artist TEXT CONSTRAINT users_music_artist_length CHECK (char_length(music_artist) BETWEEN 1 AND 100),
    ADD COLUMN background_kind TEXT,
    ADD COLUMN background_attachment_id UUID REFERENCES attachments (id) ON DELETE SET NULL,
    ADD COLUMN background_preset TEXT,
    ADD COLUMN background_colors TEXT[],
    ADD CONSTRAINT users_birthday_valid CHECK (
        (birthday_day IS NULL AND birthday_month IS NULL AND birthday_year IS NULL)
        OR (birthday_month BETWEEN 1 AND 12
            AND birthday_day BETWEEN 1 AND CASE WHEN birthday_month = 2 THEN 29 WHEN birthday_month IN (4, 6, 9, 11) THEN 30 ELSE 31 END
            AND (birthday_year IS NULL OR birthday_year BETWEEN 1900 AND 2100))),
    ADD CONSTRAINT users_background_valid CHECK (
        (background_kind IS NULL AND background_preset IS NULL AND background_colors IS NULL)
        OR (background_kind = 'media' AND background_preset IS NULL AND background_colors IS NULL)
        OR (background_kind = 'preset' AND background_attachment_id IS NULL AND background_colors IS NULL
            AND background_preset ~ '^[a-z0-9_-]{1,32}$')
        OR (background_kind = 'color' AND background_attachment_id IS NULL AND background_preset IS NULL
            AND array_to_string(background_colors, ',') ~ '^#[0-9a-f]{6}(,#[0-9a-f]{6})?$'));

CREATE INDEX users_music_attachment_idx ON users (music_attachment_id) WHERE music_attachment_id IS NOT NULL;
CREATE INDEX users_background_attachment_idx ON users (background_attachment_id) WHERE background_attachment_id IS NOT NULL;
CREATE INDEX users_birthday_idx ON users (birthday_month, birthday_day) WHERE birthday_month IS NOT NULL;

ALTER TABLE chats
    ADD COLUMN background_kind TEXT,
    ADD COLUMN background_attachment_id UUID REFERENCES attachments (id) ON DELETE SET NULL,
    ADD COLUMN background_preset TEXT,
    ADD COLUMN background_colors TEXT[],
    ADD CONSTRAINT chats_background_valid CHECK (
        (background_kind IS NULL AND background_preset IS NULL AND background_colors IS NULL)
        OR (background_kind = 'media' AND background_preset IS NULL AND background_colors IS NULL)
        OR (background_kind = 'preset' AND background_attachment_id IS NULL AND background_colors IS NULL
            AND background_preset ~ '^[a-z0-9_-]{1,32}$')
        OR (background_kind = 'color' AND background_attachment_id IS NULL AND background_preset IS NULL
            AND array_to_string(background_colors, ',') ~ '^#[0-9a-f]{6}(,#[0-9a-f]{6})?$'));

CREATE INDEX chats_background_attachment_idx ON chats (background_attachment_id) WHERE background_attachment_id IS NOT NULL;

ALTER TABLE user_privacy_settings ADD COLUMN birthday_visibility TEXT NOT NULL DEFAULT 'shared_chats'
    CHECK (birthday_visibility IN ('everyone', 'shared_chats', 'everyone_except', 'nobody_except', 'nobody'));

ALTER TABLE user_privacy_exceptions DROP CONSTRAINT user_privacy_exceptions_setting_key_check;
ALTER TABLE user_privacy_exceptions ADD CONSTRAINT user_privacy_exceptions_setting_key_check
    CHECK (setting_key IN ('avatar_visibility', 'profile_visibility', 'presence_visibility', 'last_seen_visibility',
        'birthday_visibility', 'group_invites', 'direct_messages', 'search_visibility'));

ALTER TABLE notification_preferences ADD COLUMN birthday_reminders BOOLEAN NOT NULL DEFAULT true;

CREATE TABLE birthday_reminders_sent (
    user_id     BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    receiver_id BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    year        SMALLINT    NOT NULL,
    sent_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, receiver_id, year)
);

CREATE INDEX birthday_reminders_sent_receiver_idx ON birthday_reminders_sent (receiver_id);

CREATE OR REPLACE FUNCTION bump_profile_version() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.username IS DISTINCT FROM OLD.username
        OR NEW.display_name IS DISTINCT FROM OLD.display_name
        OR NEW.bio IS DISTINCT FROM OLD.bio
        OR NEW.avatar_url IS DISTINCT FROM OLD.avatar_url
        OR NEW.avatar_attachment_id IS DISTINCT FROM OLD.avatar_attachment_id
        OR NEW.pronouns IS DISTINCT FROM OLD.pronouns
        OR NEW.birthday_day IS DISTINCT FROM OLD.birthday_day
        OR NEW.birthday_month IS DISTINCT FROM OLD.birthday_month
        OR NEW.birthday_year IS DISTINCT FROM OLD.birthday_year
        OR NEW.music_attachment_id IS DISTINCT FROM OLD.music_attachment_id
        OR NEW.music_title IS DISTINCT FROM OLD.music_title
        OR NEW.music_artist IS DISTINCT FROM OLD.music_artist
        OR NEW.background_kind IS DISTINCT FROM OLD.background_kind
        OR NEW.background_attachment_id IS DISTINCT FROM OLD.background_attachment_id
        OR NEW.background_preset IS DISTINCT FROM OLD.background_preset
        OR NEW.background_colors IS DISTINCT FROM OLD.background_colors THEN
        NEW.profile_version = OLD.profile_version + 1;
    END IF;
    RETURN NEW;
END;
$$;
