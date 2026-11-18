ALTER TABLE participants ADD COLUMN engaged BOOLEAN NOT NULL DEFAULT false;
UPDATE participants p SET engaged = true
FROM chats c
WHERE c.id = p.chat_id AND c.type = 'group';

UPDATE user_privacy_settings SET avatar_visibility = 'everyone' WHERE avatar_visibility = 'everyone_except';
UPDATE user_privacy_settings SET avatar_visibility = 'nobody' WHERE avatar_visibility = 'nobody_except';
UPDATE user_privacy_settings SET profile_visibility = 'everyone' WHERE profile_visibility = 'everyone_except';
UPDATE user_privacy_settings SET profile_visibility = 'nobody' WHERE profile_visibility = 'nobody_except';
UPDATE user_privacy_settings SET presence_visibility = 'everyone' WHERE presence_visibility = 'everyone_except';
UPDATE user_privacy_settings SET presence_visibility = 'nobody' WHERE presence_visibility = 'nobody_except';
UPDATE user_privacy_settings SET last_seen_visibility = 'everyone' WHERE last_seen_visibility = 'everyone_except';
UPDATE user_privacy_settings SET last_seen_visibility = 'nobody' WHERE last_seen_visibility = 'nobody_except';
UPDATE user_privacy_settings SET birthday_visibility = 'everyone' WHERE birthday_visibility = 'everyone_except';
UPDATE user_privacy_settings SET birthday_visibility = 'nobody' WHERE birthday_visibility = 'nobody_except';
UPDATE user_privacy_settings SET group_invites = 'everyone' WHERE group_invites = 'everyone_except';
UPDATE user_privacy_settings SET group_invites = 'nobody' WHERE group_invites = 'nobody_except';

ALTER TABLE user_privacy_settings DROP CONSTRAINT user_privacy_settings_avatar_visibility_check;
ALTER TABLE user_privacy_settings ADD CONSTRAINT user_privacy_settings_avatar_visibility_check
    CHECK (avatar_visibility IN ('everyone', 'shared_chats', 'nobody'));
ALTER TABLE user_privacy_settings DROP CONSTRAINT user_privacy_settings_profile_visibility_check;
ALTER TABLE user_privacy_settings ADD CONSTRAINT user_privacy_settings_profile_visibility_check
    CHECK (profile_visibility IN ('everyone', 'shared_chats', 'nobody'));
ALTER TABLE user_privacy_settings DROP CONSTRAINT user_privacy_settings_presence_visibility_check;
ALTER TABLE user_privacy_settings ADD CONSTRAINT user_privacy_settings_presence_visibility_check
    CHECK (presence_visibility IN ('everyone', 'shared_chats', 'nobody'));
ALTER TABLE user_privacy_settings DROP CONSTRAINT user_privacy_settings_last_seen_visibility_check;
ALTER TABLE user_privacy_settings ADD CONSTRAINT user_privacy_settings_last_seen_visibility_check
    CHECK (last_seen_visibility IN ('everyone', 'shared_chats', 'nobody'));
ALTER TABLE user_privacy_settings DROP CONSTRAINT user_privacy_settings_birthday_visibility_check;
ALTER TABLE user_privacy_settings ADD CONSTRAINT user_privacy_settings_birthday_visibility_check
    CHECK (birthday_visibility IN ('everyone', 'shared_chats', 'nobody'));
ALTER TABLE user_privacy_settings DROP CONSTRAINT user_privacy_settings_group_invites_check;
ALTER TABLE user_privacy_settings ADD CONSTRAINT user_privacy_settings_group_invites_check
    CHECK (group_invites IN ('everyone', 'shared_chats', 'nobody', 'wait_approval'));
ALTER TABLE user_privacy_settings ALTER COLUMN group_invites SET DEFAULT 'shared_chats';

ALTER TABLE user_privacy_settings DROP CONSTRAINT user_privacy_settings_direct_messages_check;
ALTER TABLE user_privacy_settings ADD CONSTRAINT user_privacy_settings_direct_messages_check
    CHECK (direct_messages IN ('everyone', 'shared_chats', 'wait_approval', 'nobody'));

ALTER TABLE user_privacy_settings ADD COLUMN calls_from TEXT NOT NULL DEFAULT 'everyone'
    CHECK (calls_from IN ('everyone', 'shared_chats', 'nobody'));
UPDATE user_privacy_settings SET calls_from = 'nobody' WHERE NOT calls_enabled;
ALTER TABLE user_privacy_settings DROP COLUMN calls_enabled;

ALTER TABLE user_privacy_exceptions DROP CONSTRAINT user_privacy_exceptions_setting_key_check;
ALTER TABLE user_privacy_exceptions ADD CONSTRAINT user_privacy_exceptions_setting_key_check
    CHECK (setting_key IN ('avatar_visibility', 'profile_visibility', 'presence_visibility', 'last_seen_visibility',
        'birthday_visibility', 'group_invites', 'direct_messages', 'search_visibility', 'calls_from'));
