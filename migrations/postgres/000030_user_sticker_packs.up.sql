ALTER TABLE sticker_packs ADD COLUMN slug TEXT UNIQUE CHECK (slug ~ '^[A-Za-z0-9]{8,32}$');
ALTER TABLE sticker_packs ADD COLUMN hidden_at TIMESTAMPTZ;
ALTER TABLE sticker_packs ADD CONSTRAINT sticker_packs_owned_or_builtin CHECK ((owner_id IS NULL) OR (builtin_key IS NULL));

CREATE TABLE sticker_pack_reports (
    pack_id     BIGINT      NOT NULL REFERENCES sticker_packs (id) ON DELETE CASCADE,
    reporter_id BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    reason      TEXT        NOT NULL CHECK (reason IN ('spam', 'violence', 'sexual', 'hate', 'copyright', 'other')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (pack_id, reporter_id)
);

CREATE INDEX sticker_pack_reports_reporter_idx ON sticker_pack_reports (reporter_id, created_at);
