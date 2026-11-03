ALTER TABLE user_privacy_exceptions DROP CONSTRAINT user_privacy_exceptions_setting_key_check;
ALTER TABLE user_privacy_exceptions ADD CONSTRAINT user_privacy_exceptions_setting_key_check
    CHECK (setting_key IN ('avatar_visibility', 'profile_visibility', 'presence_visibility', 'group_invites', 'direct_messages', 'search_visibility'));
