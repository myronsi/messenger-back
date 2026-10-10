-- name: CreateAttachment :one
INSERT INTO attachments (uploader_id, chat_id, storage_key, thumbnail_key, mime_type, size, width, height, duration, waveform, purpose, kind, filename)
VALUES (@uploader_id, @chat_id, @storage_key, @thumbnail_key, @mime_type, @size, @width, @height, @duration, @waveform, @purpose, @kind, @filename)
RETURNING *;

-- name: GetAttachment :one
SELECT * FROM attachments WHERE id = @id;

-- name: ListChatAttachments :many
SELECT * FROM attachments
WHERE chat_id = @chat_id
ORDER BY created_at DESC, id DESC
LIMIT @max_rows;

-- name: DeleteAttachment :one
DELETE FROM attachments WHERE id = @id RETURNING storage_key, thumbnail_key;

-- name: ListAttachmentKeysOfChats :many
SELECT storage_key FROM attachments WHERE chat_id = ANY(@chat_ids::bigint[]) ORDER BY storage_key;

-- name: LinkAttachment :exec
INSERT INTO attachment_links (attachment_id, chat_id, message_id) VALUES (@attachment_id, @chat_id, @message_id)
ON CONFLICT DO NOTHING;

-- name: AttachmentLinksForViewer :many
-- The messages through which the viewer could reach the attachment: links into chats the viewer is in.
SELECT l.chat_id, l.message_id
FROM attachment_links l
JOIN participants p ON p.chat_id = l.chat_id AND p.user_id = @viewer_id
WHERE l.attachment_id = @attachment_id
ORDER BY l.created_at DESC
LIMIT 50;

-- name: ListLinkedAttachments :many
-- Attachments of a chat by kind, newest first, for the media lists (photos, audio).
SELECT a.*, l.message_id, l.created_at AS linked_at
FROM attachment_links l
JOIN attachments a ON a.id = l.attachment_id
WHERE l.chat_id = @chat_id AND a.kind = ANY(@kinds::text[])
  AND (sqlc.narg(before)::timestamptz IS NULL OR l.created_at < sqlc.narg(before)::timestamptz)
ORDER BY l.created_at DESC
LIMIT @max_rows;

-- name: ListUnreferencedAttachments :many
-- Uploads older than the cut-off that no message, avatar or avatar history uses (v1 rows are bound by chat_id).
SELECT a.id, a.storage_key, a.thumbnail_key
FROM attachments a
WHERE a.created_at < @older_than
  AND a.chat_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM attachment_links l WHERE l.attachment_id = a.id)
  AND NOT EXISTS (SELECT 1 FROM users u WHERE u.avatar_attachment_id = a.id)
  AND NOT EXISTS (SELECT 1 FROM chats c WHERE c.avatar_attachment_id = a.id)
  AND NOT EXISTS (SELECT 1 FROM user_avatar_history h WHERE h.attachment_id = a.id)
ORDER BY a.created_at
LIMIT @max_rows;

-- name: DeleteAttachmentIfUnreferenced :one
-- Deletes the row only if it is still unreferenced (a send may have linked it since it was listed).
DELETE FROM attachments a
WHERE a.id = @id
  AND NOT EXISTS (SELECT 1 FROM attachment_links l WHERE l.attachment_id = a.id)
  AND NOT EXISTS (SELECT 1 FROM users u WHERE u.avatar_attachment_id = a.id)
  AND NOT EXISTS (SELECT 1 FROM chats c WHERE c.avatar_attachment_id = a.id)
  AND NOT EXISTS (SELECT 1 FROM user_avatar_history h WHERE h.attachment_id = a.id)
RETURNING a.storage_key, a.thumbnail_key;

-- name: SetUserAvatar :execrows
UPDATE users SET avatar_attachment_id = sqlc.narg(attachment_id) WHERE id = @user_id;

-- name: ClearCurrentAvatar :exec
UPDATE user_avatar_history SET is_current = FALSE WHERE user_id = @user_id AND is_current;

-- name: AddAvatarHistory :exec
INSERT INTO user_avatar_history (user_id, attachment_id, is_current) VALUES (@user_id, @attachment_id, TRUE);

-- name: ListAvatarHistory :many
SELECT h.id, h.attachment_id, h.created_at, h.is_current
FROM user_avatar_history h
WHERE h.user_id = @user_id AND h.attachment_id IS NOT NULL
  AND (sqlc.narg(before_id)::bigint IS NULL OR h.id < sqlc.narg(before_id)::bigint)
ORDER BY h.id DESC
LIMIT @max_rows;

-- name: GetAvatarHistoryEntry :one
SELECT h.id, h.attachment_id, h.created_at, h.is_current FROM user_avatar_history h WHERE h.user_id = @user_id AND h.id = @id;

-- name: SetChatAvatar :execrows
UPDATE chats SET avatar_attachment_id = sqlc.narg(attachment_id) WHERE id = @chat_id;

-- name: ChatsWithAvatar :many
SELECT id FROM chats WHERE avatar_attachment_id = @attachment_id;
