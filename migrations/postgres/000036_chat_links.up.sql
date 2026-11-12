CREATE TABLE chat_links (
    chat_id    BIGINT      NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    message_id BIGINT      NOT NULL,
    url        TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (chat_id, message_id, url)
);
