DROP TABLE IF EXISTS upload_sessions;
DROP TABLE IF EXISTS media_jobs;

ALTER TABLE attachments
    DROP COLUMN IF EXISTS audio_album,
    DROP COLUMN IF EXISTS audio_artist,
    DROP COLUMN IF EXISTS audio_title,
    DROP COLUMN IF EXISTS processing_state,
    DROP COLUMN IF EXISTS placeholder,
    DROP COLUMN IF EXISTS variants;
