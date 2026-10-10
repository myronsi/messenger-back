-- Schema v2, part 7: the upload flow of the contract. A file is uploaded first (no chat yet) and linked to a
-- message when it is sent, and to further chats when it is forwarded. Avatars are attachments as well.
-- Uploads that nothing references after a day are deleted by the worker.

ALTER TABLE attachments ALTER COLUMN chat_id DROP NOT NULL;
ALTER TABLE attachments
    ADD COLUMN purpose       TEXT NOT NULL DEFAULT 'message' CHECK (purpose IN ('message', 'avatar')),
    ADD COLUMN kind          TEXT NOT NULL DEFAULT 'file' CHECK (kind IN ('image', 'audio', 'voice', 'video', 'file')),
    -- Shown to clients only; never used to build a storage key or decide the type.
    ADD COLUMN filename      TEXT NOT NULL DEFAULT 'file' CHECK (char_length(filename) BETWEEN 1 AND 255),
    ADD COLUMN thumbnail_key TEXT;

CREATE UNIQUE INDEX attachments_thumbnail_key_key ON attachments (thumbnail_key);
-- The garbage collection of unreferenced uploads walks them by age.
CREATE INDEX attachments_created_idx ON attachments (created_at);

-- Which messages use an attachment. Access to a file follows from these links: members of the chat who can see
-- the message may download it.
CREATE TABLE attachment_links (
    attachment_id UUID        NOT NULL REFERENCES attachments (id) ON DELETE CASCADE,
    chat_id       BIGINT      NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    -- Messages live in ScyllaDB, so this is not a foreign key.
    message_id    BIGINT      NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (attachment_id, chat_id, message_id)
);

CREATE INDEX attachment_links_chat_idx ON attachment_links (chat_id, created_at DESC);

ALTER TABLE users ADD COLUMN avatar_attachment_id UUID REFERENCES attachments (id) ON DELETE SET NULL;
CREATE INDEX users_avatar_attachment_idx ON users (avatar_attachment_id) WHERE avatar_attachment_id IS NOT NULL;

ALTER TABLE chats ADD COLUMN avatar_attachment_id UUID REFERENCES attachments (id) ON DELETE SET NULL;
CREATE INDEX chats_avatar_attachment_idx ON chats (avatar_attachment_id) WHERE avatar_attachment_id IS NOT NULL;

ALTER TABLE user_avatar_history ALTER COLUMN avatar_url DROP NOT NULL;
ALTER TABLE user_avatar_history ADD COLUMN attachment_id UUID REFERENCES attachments (id) ON DELETE CASCADE;
CREATE INDEX user_avatar_history_attachment_idx ON user_avatar_history (attachment_id) WHERE attachment_id IS NOT NULL;
