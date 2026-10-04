-- Schema v2, part 5: uploaded files. The bytes live in object storage under storage_key; this table is
-- the metadata and the access-control anchor (a file belongs to a chat).

CREATE TABLE attachments (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Kept when the uploader deletes the account: other members still see the message.
    uploader_id BIGINT REFERENCES users (id) ON DELETE SET NULL,
    chat_id     BIGINT           NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    storage_key TEXT             NOT NULL,
    mime_type   TEXT             NOT NULL,
    size        BIGINT           NOT NULL CHECK (size >= 0),
    -- Images and video.
    width       INTEGER CHECK (width > 0),
    height      INTEGER CHECK (height > 0),
    -- Audio and video, in seconds.
    duration    DOUBLE PRECISION CHECK (duration >= 0),
    -- Audio: bar heights for the voice-message preview.
    waveform    SMALLINT[],
    created_at  TIMESTAMPTZ      NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX attachments_storage_key_key ON attachments (storage_key);
CREATE INDEX attachments_chat_idx ON attachments (chat_id, created_at DESC);
CREATE INDEX attachments_uploader_idx ON attachments (uploader_id);
