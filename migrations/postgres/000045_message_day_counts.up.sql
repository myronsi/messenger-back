CREATE TABLE message_day_counts (
    day         DATE   NOT NULL,
    type        TEXT   NOT NULL CHECK (type IN ('text', 'file', 'voice', 'sticker')),
    messages    BIGINT NOT NULL DEFAULT 0 CHECK (messages >= 0),
    attachments BIGINT NOT NULL DEFAULT 0 CHECK (attachments >= 0),
    PRIMARY KEY (day, type)
);
