DROP TABLE sticker_pack_reports;

ALTER TABLE sticker_packs DROP CONSTRAINT sticker_packs_owned_or_builtin;
ALTER TABLE sticker_packs DROP COLUMN hidden_at;
ALTER TABLE sticker_packs DROP COLUMN slug;
