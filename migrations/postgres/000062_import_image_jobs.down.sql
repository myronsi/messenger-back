DROP INDEX IF EXISTS media_jobs_import_running_idx;
DELETE FROM media_jobs WHERE kind = 'import_image';
ALTER TABLE media_jobs DROP CONSTRAINT media_jobs_kind_check;
ALTER TABLE media_jobs ADD CONSTRAINT media_jobs_kind_check CHECK (kind IN ('video', 'gif', 'audio', 'faststart'));
