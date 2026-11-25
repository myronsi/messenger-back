CREATE TABLE chat_imports (
    id               BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chat_id          BIGINT      NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    imported_by      BIGINT      REFERENCES users (id) ON DELETE SET NULL,
    source           TEXT        NOT NULL CHECK (source IN ('telegram', 'whatsapp')),
    state            TEXT        NOT NULL CHECK (state IN ('running', 'done', 'removing')),
    message_count    INTEGER     NOT NULL CHECK (message_count BETWEEN 1 AND 100000),
    file_count       INTEGER     NOT NULL CHECK (file_count BETWEEN 0 AND 5000),
    first_at         TIMESTAMPTZ NOT NULL,
    last_at          TIMESTAMPTZ NOT NULL,
    lower_bound      BIGINT      NOT NULL,
    first_message_id BIGINT      NOT NULL,
    last_message_id  BIGINT      NOT NULL,
    next_index       INTEGER     NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at      TIMESTAMPTZ,
    CHECK (first_at <= last_at),
    CHECK (first_message_id < last_message_id)
);

CREATE INDEX chat_imports_chat_idx ON chat_imports (chat_id, first_message_id);

CREATE INDEX chat_imports_imported_by_idx ON chat_imports (imported_by);

CREATE UNIQUE INDEX chat_imports_running_idx ON chat_imports (chat_id) WHERE state = 'running';

CREATE TABLE chat_import_senders (
    import_id         BIGINT      NOT NULL REFERENCES chat_imports (id) ON DELETE CASCADE,
    sender_key        TEXT        NOT NULL CHECK (char_length(sender_key) BETWEEN 1 AND 64),
    name              TEXT        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 128),
    message_count     INTEGER     NOT NULL DEFAULT 0 CHECK (message_count >= 0),
    assigned_user_id  BIGINT      REFERENCES users (id) ON DELETE SET NULL,
    state             TEXT        NOT NULL CHECK (state IN ('me', 'unassigned', 'pending', 'confirmed', 'declined')),
    declined_user_ids BIGINT[]    NOT NULL DEFAULT '{}',
    decided_at        TIMESTAMPTZ,
    PRIMARY KEY (import_id, sender_key)
);

CREATE INDEX chat_import_senders_user_idx ON chat_import_senders (assigned_user_id);

CREATE TABLE chat_import_windows (
    import_id BIGINT    NOT NULL REFERENCES chat_imports (id) ON DELETE CASCADE,
    chunk     INTEGER   NOT NULL CHECK (chunk >= 0),
    node      INTEGER   NOT NULL CHECK (node BETWEEN 1008 AND 1023),
    start_ms  BIGINT    NOT NULL CHECK (start_ms > 0),
    size      INTEGER   NOT NULL CHECK (size BETWEEN 1 AND 5000),
    span      INT8RANGE GENERATED ALWAYS AS (int8range((node::bigint << 41) + start_ms, (node::bigint << 41) + start_ms + (size + 4095) / 4096)) STORED,
    PRIMARY KEY (import_id, chunk),
    EXCLUDE USING gist (span WITH &&)
);

ALTER TABLE sync_changes DROP CONSTRAINT sync_changes_kind_check;
ALTER TABLE sync_changes ADD CONSTRAINT sync_changes_kind_check
    CHECK (kind IN ('message_edited', 'message_deleted', 'message_hidden', 'reactions', 'pins', 'history_cleared', 'chat_hidden', 'history_imported'));
