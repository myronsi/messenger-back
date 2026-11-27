DROP INDEX IF EXISTS chat_imports_running_by_idx;
DROP INDEX IF EXISTS chat_imports_idle_idx;
ALTER TABLE chat_imports DROP COLUMN IF EXISTS last_activity_at;
