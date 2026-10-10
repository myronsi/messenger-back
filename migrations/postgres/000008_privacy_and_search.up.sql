-- Schema v2, part 8: the privacy values of the contract and user search.

-- The contract allows "nobody" for every visibility; v1 had it only for presence.
ALTER TABLE user_privacy_settings DROP CONSTRAINT user_privacy_settings_avatar_visibility_check;
ALTER TABLE user_privacy_settings ADD CONSTRAINT user_privacy_settings_avatar_visibility_check
    CHECK (avatar_visibility IN ('everyone', 'shared_chats', 'everyone_except', 'nobody_except', 'nobody'));
ALTER TABLE user_privacy_settings DROP CONSTRAINT user_privacy_settings_profile_visibility_check;
ALTER TABLE user_privacy_settings ADD CONSTRAINT user_privacy_settings_profile_visibility_check
    CHECK (profile_visibility IN ('everyone', 'shared_chats', 'everyone_except', 'nobody_except', 'nobody'));

-- User search: a prefix of the username, or any part of the display name.
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX users_username_prefix_idx ON users (LOWER(username) text_pattern_ops);
CREATE INDEX users_display_name_trgm_idx ON users USING gin (LOWER(display_name) gin_trgm_ops);
