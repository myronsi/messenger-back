-- Schema v2, part 10: the media lists of a chat page by message id.
CREATE INDEX attachment_links_chat_message_idx ON attachment_links (chat_id, message_id DESC);
