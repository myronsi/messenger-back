CREATE TABLE chat_deletions (
    chat_id    BIGINT      PRIMARY KEY,
    deleted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    emitted_at TIMESTAMPTZ
);
