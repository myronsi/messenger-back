-- Messages and everything attached to them. PostgreSQL holds chats, members and attachment metadata.
-- The keyspace is created outside the migrations (replication differs per environment, see
-- docs/messages.md). Statements are separated by semicolons, so comments must not contain one.

-- One partition per chat and 10-day bucket keeps partitions small however old a chat gets.
-- bucket = milliseconds since the Unix epoch of the message time / 864000000.
CREATE TABLE IF NOT EXISTS messages (
    chat_id                    bigint,
    bucket                     int,
    message_id                 bigint,
    sender_id                  bigint,
    type                       text,
    content                    text,
    attachment_id              uuid,
    reply_to                   bigint,
    forwarded_from_message_id  bigint,
    forwarded_from_sender_id   bigint,
    forwarded_from_sender_name text,
    edited_at                  timestamp,
    deleted                    boolean,
    created_at                 timestamp,
    PRIMARY KEY ((chat_id, bucket), message_id)
) WITH CLUSTERING ORDER BY (message_id DESC);

-- The buckets of a chat that contain messages, for paging across empty stretches.
CREATE TABLE IF NOT EXISTS chat_buckets (
    chat_id bigint,
    bucket  int,
    PRIMARY KEY (chat_id, bucket)
) WITH CLUSTERING ORDER BY (bucket DESC);

-- Finds a message by its id alone (edit, delete, reply, jump to, forward).
CREATE TABLE IF NOT EXISTS message_locations (
    message_id bigint PRIMARY KEY,
    chat_id    bigint,
    bucket     int
);

-- One row per user and reaction, so concurrent reactions never overwrite each other.
CREATE TABLE IF NOT EXISTS message_reactions (
    chat_id    bigint,
    message_id bigint,
    reaction   text,
    user_id    bigint,
    created_at timestamp,
    PRIMARY KEY ((chat_id, message_id), reaction, user_id)
);

-- Messages a user does not see: deleted for that user only, or never delivered to them.
-- Replaces the deleted_for and undelivered_to JSON arrays of v1.
CREATE TABLE IF NOT EXISTS hidden_messages (
    user_id    bigint,
    chat_id    bigint,
    message_id bigint,
    reason     text,
    PRIMARY KEY ((user_id, chat_id), message_id)
) WITH CLUSTERING ORDER BY (message_id DESC);

-- Who has hidden messages in a chat, so deleting the chat can find the hidden_messages partitions.
CREATE TABLE IF NOT EXISTS hidden_message_users (
    chat_id bigint,
    user_id bigint,
    PRIMARY KEY (chat_id, user_id)
);
