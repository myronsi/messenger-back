ALTER TABLE user_moderation
    ADD COLUMN decided_by  TEXT        NOT NULL DEFAULT 'automatic' CHECK (decided_by IN ('automatic', 'staff')),
    ADD COLUMN decided_at  TIMESTAMPTZ,
    ADD COLUMN reason_code TEXT        CHECK (reason_code IN ('spam', 'scam', 'harassment', 'impersonation', 'inappropriate_profile', 'illegal', 'other')),
    ADD COLUMN reason_text TEXT        CHECK (char_length(reason_text) <= 1000);

ALTER TABLE user_moderation_events
    ADD COLUMN reason_code TEXT,
    ADD COLUMN reason_text TEXT,
    ADD COLUMN staff_ref   TEXT;

ALTER TABLE user_reports ADD COLUMN dismissed_at TIMESTAMPTZ;

ALTER TABLE sticker_packs ADD COLUMN hidden_by TEXT CHECK (hidden_by IN ('reports', 'staff'));
UPDATE sticker_packs SET hidden_by = 'reports' WHERE hidden_at IS NOT NULL;

ALTER TABLE emoji_packs ADD COLUMN hidden_by TEXT CHECK (hidden_by IN ('reports', 'staff'));
UPDATE emoji_packs SET hidden_by = 'reports' WHERE hidden_at IS NOT NULL;

CREATE TABLE report_outcomes (
    id           BIGSERIAL   PRIMARY KEY,
    reporter_id  BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    subject_type TEXT        NOT NULL CHECK (subject_type IN ('user', 'sticker_pack', 'emoji_pack')),
    subject_id   BIGINT      NOT NULL,
    outcome      TEXT        NOT NULL CHECK (outcome IN ('action_taken', 'no_violation')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    read_at      TIMESTAMPTZ
);

CREATE INDEX report_outcomes_reporter_idx ON report_outcomes (reporter_id, id);
CREATE INDEX report_outcomes_subject_idx ON report_outcomes (subject_type, subject_id, reporter_id);
CREATE INDEX report_outcomes_created_idx ON report_outcomes (created_at);
