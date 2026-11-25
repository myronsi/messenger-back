DROP TABLE IF EXISTS poll_votes;
DROP TABLE IF EXISTS polls;

DELETE FROM sync_changes WHERE kind = 'poll';
ALTER TABLE sync_changes DROP CONSTRAINT sync_changes_kind_check;
ALTER TABLE sync_changes ADD CONSTRAINT sync_changes_kind_check
    CHECK (kind IN ('message_edited', 'message_deleted', 'message_hidden', 'reactions', 'pins', 'history_cleared', 'chat_hidden'));

ALTER TABLE unsent_messages DROP COLUMN IF EXISTS payload;
DELETE FROM unsent_messages WHERE type IN ('poll', 'contact', 'location');
ALTER TABLE unsent_messages DROP CONSTRAINT unsent_messages_type_check;
ALTER TABLE unsent_messages ADD CONSTRAINT unsent_messages_type_check
    CHECK (type IN ('text', 'file', 'voice', 'sticker'));

DELETE FROM message_day_counts WHERE type IN ('poll', 'contact', 'location');
ALTER TABLE message_day_counts DROP CONSTRAINT message_day_counts_type_check;
ALTER TABLE message_day_counts ADD CONSTRAINT message_day_counts_type_check
    CHECK (type IN ('text', 'file', 'voice', 'sticker'));
