CREATE TABLE content_reports (
    id           BIGSERIAL    PRIMARY KEY,
    reporter_id  BIGINT       REFERENCES users (id) ON DELETE SET NULL,
    kind         TEXT         NOT NULL CHECK (kind IN ('message', 'group')),
    chat_id      BIGINT       NOT NULL,
    message_id   BIGINT       NOT NULL DEFAULT 0,
    target_id    BIGINT       REFERENCES users (id) ON DELETE SET NULL,
    reason       TEXT         NOT NULL CHECK (reason IN ('spam', 'scam', 'harassment', 'impersonation', 'inappropriate_profile', 'illegal', 'other')),
    note         TEXT         CHECK (char_length(note) <= 1000),
    content      JSONB        NOT NULL,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    withdrawn_at TIMESTAMPTZ,
    CHECK ((kind = 'group') = (message_id = 0)),
    CHECK (reason <> 'illegal' OR char_length(btrim(coalesce(note, ''))) > 0)
);

CREATE UNIQUE INDEX content_reports_reporter_key ON content_reports (reporter_id, chat_id, message_id);
CREATE INDEX content_reports_reporter_idx ON content_reports (reporter_id, updated_at);
CREATE INDEX content_reports_target_idx ON content_reports (target_id, created_at);
CREATE INDEX content_reports_chat_idx ON content_reports (chat_id, message_id);
CREATE INDEX content_reports_created_idx ON content_reports (created_at);
