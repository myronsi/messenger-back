ALTER TABLE message_day_counts DROP CONSTRAINT message_day_counts_type_check;
ALTER TABLE message_day_counts ADD CONSTRAINT message_day_counts_type_check
    CHECK (type IN ('text', 'file', 'voice', 'sticker', 'poll', 'contact', 'location'));

ALTER TABLE unsent_messages DROP CONSTRAINT unsent_messages_type_check;
ALTER TABLE unsent_messages ADD CONSTRAINT unsent_messages_type_check
    CHECK (type IN ('text', 'file', 'voice', 'sticker', 'poll', 'contact', 'location'));
ALTER TABLE unsent_messages ADD COLUMN payload JSONB;

ALTER TABLE sync_changes DROP CONSTRAINT sync_changes_kind_check;
ALTER TABLE sync_changes ADD CONSTRAINT sync_changes_kind_check
    CHECK (kind IN ('message_edited', 'message_deleted', 'message_hidden', 'reactions', 'pins', 'history_cleared', 'chat_hidden', 'poll'));

CREATE TABLE polls (
    message_id       BIGINT      PRIMARY KEY,
    chat_id          BIGINT      NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    creator_id       BIGINT      NOT NULL,
    question         TEXT        NOT NULL,
    options          TEXT[]      NOT NULL CHECK (cardinality(options) BETWEEN 2 AND 10),
    multiple_answers BOOLEAN     NOT NULL DEFAULT false,
    anonymous        BOOLEAN     NOT NULL DEFAULT true,
    correct_option   SMALLINT,
    explanation      TEXT,
    close_at         TIMESTAMPTZ,
    closed_at        TIMESTAMPTZ,
    closed_by        BIGINT,
    version          INTEGER     NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (correct_option IS NULL OR (correct_option >= 0 AND correct_option < cardinality(options) AND NOT multiple_answers))
);

CREATE INDEX polls_chat_idx ON polls (chat_id);
CREATE INDEX polls_close_idx ON polls (close_at) WHERE closed_at IS NULL AND close_at IS NOT NULL;

CREATE TABLE poll_votes (
    message_id BIGINT      NOT NULL REFERENCES polls (message_id) ON DELETE CASCADE,
    user_id    BIGINT      NOT NULL,
    option     SMALLINT    NOT NULL CHECK (option BETWEEN 0 AND 9),
    voted_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (message_id, user_id, option)
);

CREATE INDEX poll_votes_option_idx ON poll_votes (message_id, option, voted_at DESC);
