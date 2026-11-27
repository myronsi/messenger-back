DELETE FROM media_jobs WHERE kind = 'import_sticker';
ALTER TABLE media_jobs DROP CONSTRAINT media_jobs_kind_check;
ALTER TABLE media_jobs ADD CONSTRAINT media_jobs_kind_check CHECK (kind IN ('video', 'gif', 'audio', 'faststart', 'import_image'));
