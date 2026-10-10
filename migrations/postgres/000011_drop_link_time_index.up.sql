-- Schema v2, part 11: the media lists no longer page by link time (000010), so that index only costs writes.
DROP INDEX CONCURRENTLY IF EXISTS attachment_links_chat_idx;
