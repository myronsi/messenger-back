DROP TABLE report_outcomes;

ALTER TABLE emoji_packs DROP COLUMN hidden_by;
ALTER TABLE sticker_packs DROP COLUMN hidden_by;

ALTER TABLE user_reports DROP COLUMN dismissed_at;

ALTER TABLE user_moderation_events
    DROP COLUMN staff_ref,
    DROP COLUMN reason_text,
    DROP COLUMN reason_code;

ALTER TABLE user_moderation
    DROP COLUMN reason_text,
    DROP COLUMN reason_code,
    DROP COLUMN decided_at,
    DROP COLUMN decided_by;
