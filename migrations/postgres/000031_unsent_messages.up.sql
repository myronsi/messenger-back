-- Sends the server rejected for a reason that may pass later (privacy, a block, a rate limit, an outage). They are
-- kept for the sender's devices until a send with the same client_temp_id succeeds or the sender discards them.
CREATE TABLE unsent_messages (
    user_id        BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    chat_id        BIGINT      NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    client_temp_id TEXT        NOT NULL CHECK (char_length(client_temp_id) BETWEEN 1 AND 64),
    type           TEXT        NOT NULL CHECK (type IN ('text', 'file', 'voice', 'sticker')),
    content        TEXT,
    reply_to       BIGINT,
    attachment_ids UUID[]      NOT NULL DEFAULT '{}',
    drawings       JSONB,
    sticker_id     BIGINT,
    error_code     TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, chat_id, client_temp_id)
);

CREATE INDEX unsent_messages_user_idx ON unsent_messages (user_id, created_at);
CREATE INDEX unsent_messages_attachments_idx ON unsent_messages USING GIN (attachment_ids);
