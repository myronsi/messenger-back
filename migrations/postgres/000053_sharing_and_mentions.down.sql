ALTER TABLE notification_preferences DROP COLUMN IF EXISTS mentions_in_muted;
ALTER TABLE user_privacy_settings DROP COLUMN IF EXISTS web_profile_visibility;

DROP TRIGGER IF EXISTS participants_clear_mentions ON participants;
DROP FUNCTION IF EXISTS participants_clear_mentions();
DROP TABLE IF EXISTS unread_mentions;

DROP TABLE IF EXISTS chat_bans;
DROP TABLE IF EXISTS chat_invite_links;

DROP TRIGGER IF EXISTS chats_sync_public_name ON chats;
DROP FUNCTION IF EXISTS chats_sync_public_name();
DROP INDEX IF EXISTS chats_public_name_search_idx;
ALTER TABLE chats DROP COLUMN IF EXISTS public_name;

DROP TRIGGER IF EXISTS users_sync_public_name ON users;
DROP FUNCTION IF EXISTS users_sync_public_name();
DROP TABLE IF EXISTS public_names;
