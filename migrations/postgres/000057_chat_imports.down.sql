DELETE FROM sync_changes WHERE kind = 'history_imported';
ALTER TABLE sync_changes DROP CONSTRAINT sync_changes_kind_check;
ALTER TABLE sync_changes ADD CONSTRAINT sync_changes_kind_check
    CHECK (kind IN ('message_edited', 'message_deleted', 'message_hidden', 'reactions', 'pins', 'history_cleared', 'chat_hidden'));

DROP TABLE IF EXISTS chat_import_windows;
DROP TABLE IF EXISTS chat_import_senders;
DROP TABLE IF EXISTS chat_imports;
