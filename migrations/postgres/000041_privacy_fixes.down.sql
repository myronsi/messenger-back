DELETE FROM user_privacy_exceptions WHERE setting_key = 'calls_from';
ALTER TABLE user_privacy_exceptions DROP CONSTRAINT user_privacy_exceptions_setting_key_check;
ALTER TABLE user_privacy_exceptions ADD CONSTRAINT user_privacy_exceptions_setting_key_check
    CHECK (setting_key IN ('avatar_visibility', 'profile_visibility', 'presence_visibility', 'last_seen_visibility',
        'birthday_visibility', 'group_invites', 'direct_messages', 'search_visibility'));

ALTER TABLE user_privacy_settings ADD COLUMN calls_enabled BOOLEAN NOT NULL DEFAULT true;
UPDATE user_privacy_settings SET calls_enabled = false WHERE calls_from = 'nobody';
ALTER TABLE user_privacy_settings DROP COLUMN IF EXISTS calls_from;

UPDATE user_privacy_settings SET direct_messages = 'wait_approval' WHERE direct_messages = 'nobody';
ALTER TABLE user_privacy_settings DROP CONSTRAINT user_privacy_settings_direct_messages_check;
ALTER TABLE user_privacy_settings ADD CONSTRAINT user_privacy_settings_direct_messages_check
    CHECK (direct_messages IN ('everyone', 'shared_chats', 'wait_approval'));

ALTER TABLE user_privacy_settings ALTER COLUMN group_invites SET DEFAULT 'everyone';
ALTER TABLE user_privacy_settings DROP CONSTRAINT user_privacy_settings_group_invites_check;
ALTER TABLE user_privacy_settings ADD CONSTRAINT user_privacy_settings_group_invites_check
    CHECK (group_invites IN ('everyone', 'shared_chats', 'everyone_except', 'nobody_except', 'nobody', 'wait_approval'));
ALTER TABLE user_privacy_settings DROP CONSTRAINT user_privacy_settings_birthday_visibility_check;
ALTER TABLE user_privacy_settings ADD CONSTRAINT user_privacy_settings_birthday_visibility_check
    CHECK (birthday_visibility IN ('everyone', 'shared_chats', 'everyone_except', 'nobody_except', 'nobody'));
ALTER TABLE user_privacy_settings DROP CONSTRAINT user_privacy_settings_last_seen_visibility_check;
ALTER TABLE user_privacy_settings ADD CONSTRAINT user_privacy_settings_last_seen_visibility_check
    CHECK (last_seen_visibility IN ('everyone', 'shared_chats', 'everyone_except', 'nobody_except', 'nobody'));
ALTER TABLE user_privacy_settings DROP CONSTRAINT user_privacy_settings_presence_visibility_check;
ALTER TABLE user_privacy_settings ADD CONSTRAINT user_privacy_settings_presence_visibility_check
    CHECK (presence_visibility IN ('everyone', 'shared_chats', 'everyone_except', 'nobody_except', 'nobody'));
ALTER TABLE user_privacy_settings DROP CONSTRAINT user_privacy_settings_profile_visibility_check;
ALTER TABLE user_privacy_settings ADD CONSTRAINT user_privacy_settings_profile_visibility_check
    CHECK (profile_visibility IN ('everyone', 'shared_chats', 'everyone_except', 'nobody_except', 'nobody'));
ALTER TABLE user_privacy_settings DROP CONSTRAINT user_privacy_settings_avatar_visibility_check;
ALTER TABLE user_privacy_settings ADD CONSTRAINT user_privacy_settings_avatar_visibility_check
    CHECK (avatar_visibility IN ('everyone', 'shared_chats', 'everyone_except', 'nobody_except', 'nobody'));

ALTER TABLE participants DROP COLUMN IF EXISTS engaged;
