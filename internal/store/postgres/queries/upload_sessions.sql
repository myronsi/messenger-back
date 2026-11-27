-- name: CreateUploadSession :one
INSERT INTO upload_sessions (uploader_id, purpose, kind, filename, size, chunk_size)
VALUES (@uploader_id, @purpose, @kind, @filename, @size, @chunk_size)
RETURNING *;

-- name: GetUploadSession :one
SELECT * FROM upload_sessions WHERE id = @id AND uploader_id = @uploader_id;

-- name: AdvanceUploadSession :one
UPDATE upload_sessions
SET received = received + @length::bigint, chunks = chunks + 1, updated_at = now()
WHERE id = @id AND uploader_id = @uploader_id AND received = @offset_bytes::bigint
  AND received + @length::bigint <= size
RETURNING *;

-- name: DeleteUploadSession :one
DELETE FROM upload_sessions WHERE id = @id RETURNING *;

-- name: CountUploadSessions :one
SELECT count(*)::bigint FROM upload_sessions WHERE uploader_id = @uploader_id;

-- name: ListIdleUploadSessionsOf :many
SELECT * FROM upload_sessions WHERE uploader_id = @uploader_id AND updated_at < @updated_before ORDER BY updated_at LIMIT @max_rows;

-- name: ListStaleUploadSessions :many
SELECT * FROM upload_sessions WHERE updated_at < @updated_before ORDER BY updated_at LIMIT @max_rows;
