DROP INDEX IF EXISTS participants_requests_idx;

ALTER TABLE participants DROP COLUMN IF EXISTS is_request;
