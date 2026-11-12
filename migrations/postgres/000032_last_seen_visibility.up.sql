ALTER TABLE user_privacy_settings ADD COLUMN last_seen_visibility TEXT NOT NULL DEFAULT 'everyone'
    CHECK (last_seen_visibility IN ('everyone', 'shared_chats', 'everyone_except', 'nobody_except', 'nobody'));
UPDATE user_privacy_settings SET last_seen_visibility = presence_visibility
WHERE last_seen_visibility IS DISTINCT FROM presence_visibility;

ALTER TABLE user_privacy_exceptions DROP CONSTRAINT user_privacy_exceptions_setting_key_check;
ALTER TABLE user_privacy_exceptions ADD CONSTRAINT user_privacy_exceptions_setting_key_check
    CHECK (setting_key IN ('avatar_visibility', 'profile_visibility', 'presence_visibility', 'last_seen_visibility',
        'group_invites', 'direct_messages', 'search_visibility'));
INSERT INTO user_privacy_exceptions (owner_id, setting_key, target_user_id, effect, created_at)
SELECT owner_id, 'last_seen_visibility', target_user_id, effect, created_at
FROM user_privacy_exceptions
WHERE setting_key = 'presence_visibility';
