CREATE TABLE sync_changes (
    seq        BIGSERIAL   PRIMARY KEY,
    chat_id    BIGINT      NOT NULL,
    user_id    BIGINT,
    kind       TEXT        NOT NULL CHECK (kind IN ('message_edited', 'message_deleted', 'message_hidden', 'reactions', 'pins', 'history_cleared', 'chat_hidden')),
    message_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX sync_changes_chat_idx ON sync_changes (chat_id, seq);
CREATE INDEX sync_changes_user_idx ON sync_changes (user_id, seq) WHERE user_id IS NOT NULL;
CREATE INDEX sync_changes_created_idx ON sync_changes (created_at);

CREATE TABLE session_storage_keys (
    session_id UUID        PRIMARY KEY REFERENCES user_sessions (id) ON DELETE CASCADE,
    sealed_key TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
