DROP TRIGGER IF EXISTS stickers_bump_pack_version ON stickers;
DROP FUNCTION IF EXISTS bump_sticker_pack_version();
DROP TRIGGER IF EXISTS sticker_packs_bump_version ON sticker_packs;
DROP FUNCTION IF EXISTS bump_sticker_pack_title_version();
DROP TRIGGER IF EXISTS users_bump_profile_version ON users;
DROP FUNCTION IF EXISTS bump_profile_version();
ALTER TABLE sticker_packs DROP COLUMN IF EXISTS version;
ALTER TABLE users DROP COLUMN IF EXISTS profile_version;
