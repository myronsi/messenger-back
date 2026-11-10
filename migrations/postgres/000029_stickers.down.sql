DROP TABLE IF EXISTS user_recent_stickers;
DROP TABLE IF EXISTS user_sticker_packs;
DROP TABLE IF EXISTS stickers;
DROP TABLE IF EXISTS sticker_packs;
DELETE FROM attachments WHERE purpose = 'sticker';
ALTER TABLE attachments DROP CONSTRAINT attachments_purpose_check;
ALTER TABLE attachments ADD CONSTRAINT attachments_purpose_check CHECK (purpose IN ('message', 'avatar'));
