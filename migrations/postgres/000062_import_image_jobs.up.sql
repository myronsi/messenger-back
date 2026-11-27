ALTER TABLE media_jobs DROP CONSTRAINT media_jobs_kind_check;
ALTER TABLE media_jobs ADD CONSTRAINT media_jobs_kind_check CHECK (kind IN ('video', 'gif', 'audio', 'faststart', 'import_image'));
CREATE INDEX media_jobs_import_running_idx ON media_jobs (kind) WHERE state = 'running';
