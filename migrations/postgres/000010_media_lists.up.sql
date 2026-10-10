-- Schema v2, part 10: the media lists of a chat page by message id. Built concurrently: sends write
-- attachment_links, and a plain CREATE INDEX would block them on a big table (one statement per file, so
-- the migration tool runs it outside a transaction).
CREATE INDEX CONCURRENTLY IF NOT EXISTS attachment_links_chat_message_idx ON attachment_links (chat_id, message_id DESC);
