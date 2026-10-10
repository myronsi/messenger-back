DROP INDEX users_display_name_trgm_idx;
DROP INDEX users_username_prefix_idx;
-- The extension stays: other objects in the database may use it, and it is harmless.

UPDATE user_privacy_settings SET avatar_visibility = 'nobody_except' WHERE avatar_visibility = 'nobody';
UPDATE user_privacy_settings SET profile_visibility = 'nobody_except' WHERE profile_visibility = 'nobody';
ALTER TABLE user_privacy_settings DROP CONSTRAINT user_privacy_settings_profile_visibility_check;
ALTER TABLE user_privacy_settings ADD CONSTRAINT user_privacy_settings_profile_visibility_check
    CHECK (profile_visibility IN ('everyone', 'shared_chats', 'everyone_except', 'nobody_except'));
ALTER TABLE user_privacy_settings DROP CONSTRAINT user_privacy_settings_avatar_visibility_check;
ALTER TABLE user_privacy_settings ADD CONSTRAINT user_privacy_settings_avatar_visibility_check
    CHECK (avatar_visibility IN ('everyone', 'shared_chats', 'everyone_except', 'nobody_except'));
