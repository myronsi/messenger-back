ALTER TABLE chat_imports ADD COLUMN last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now();
CREATE INDEX chat_imports_idle_idx ON chat_imports (last_activity_at) WHERE state = 'running';
CREATE INDEX chat_imports_running_by_idx ON chat_imports (imported_by) WHERE state = 'running';
