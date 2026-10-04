-- name: CreateAttachment :one
INSERT INTO attachments (uploader_id, chat_id, storage_key, mime_type, size, width, height, duration, waveform)
VALUES (@uploader_id, @chat_id, @storage_key, @mime_type, @size, @width, @height, @duration, @waveform)
RETURNING *;

-- name: GetAttachment :one
SELECT * FROM attachments WHERE id = @id;

-- name: ListChatAttachments :many
SELECT * FROM attachments
WHERE chat_id = @chat_id
ORDER BY created_at DESC, id DESC
LIMIT @max_rows;

-- name: DeleteAttachment :one
DELETE FROM attachments WHERE id = @id RETURNING storage_key;

-- name: ListAttachmentKeysOfChats :many
SELECT storage_key FROM attachments WHERE chat_id = ANY(@chat_ids::bigint[]) ORDER BY storage_key;
