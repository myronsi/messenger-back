CREATE INDEX CONCURRENTLY IF NOT EXISTS attachment_links_chat_idx ON attachment_links (chat_id, created_at DESC);
