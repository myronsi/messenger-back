UPDATE users SET display_name = left(display_name, 100) WHERE char_length(display_name) > 100;
ALTER TABLE users DROP CONSTRAINT users_display_name_length;
ALTER TABLE users ADD CONSTRAINT users_display_name_length CHECK (char_length(btrim(display_name)) BETWEEN 1 AND 100);

DROP TRIGGER IF EXISTS custom_emoji_bump_pack_version ON custom_emoji;
DROP FUNCTION IF EXISTS bump_emoji_pack_version();
DROP TABLE IF EXISTS emoji_pack_reports;
DROP TABLE IF EXISTS user_emoji_packs;
DROP TABLE IF EXISTS custom_emoji;
DROP TABLE IF EXISTS emoji_packs;

DELETE FROM attachments WHERE purpose = 'emoji';
ALTER TABLE attachments DROP CONSTRAINT attachments_purpose_check;
ALTER TABLE attachments ADD CONSTRAINT attachments_purpose_check
    CHECK (purpose IN ('message', 'avatar', 'sticker', 'profile_music', 'profile_background'));
