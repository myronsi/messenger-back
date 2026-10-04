-- name: InsertDirectChat :one
-- Does nothing (and returns no row) when the pair already has a chat.
INSERT INTO chats (type, direct_key, created_by)
VALUES ('direct', @direct_key, @created_by)
ON CONFLICT (direct_key) DO NOTHING
RETURNING *;

-- name: GetDirectChat :one
SELECT * FROM chats WHERE direct_key = @direct_key;

-- name: InsertGroupChat :one
INSERT INTO chats (type, name, description, avatar_url, created_by)
VALUES ('group', @name, @description, @avatar_url, @created_by)
RETURNING *;

-- name: GetChat :one
SELECT * FROM chats WHERE id = @id;

-- name: LockChat :one
SELECT * FROM chats WHERE id = @id FOR UPDATE;

-- name: DeleteChat :execrows
DELETE FROM chats WHERE id = @id;

-- name: ListUserChats :many
SELECT c.*
FROM chats c
JOIN participants p ON p.chat_id = c.id
WHERE p.user_id = @user_id
ORDER BY c.updated_at DESC, c.id DESC
LIMIT @max_rows;

-- name: AddParticipant :one
INSERT INTO participants (chat_id, user_id, role)
VALUES (@chat_id, @user_id, @role)
RETURNING *;

-- name: GetParticipant :one
SELECT * FROM participants WHERE chat_id = @chat_id AND user_id = @user_id;

-- name: LockParticipant :one
SELECT * FROM participants WHERE chat_id = @chat_id AND user_id = @user_id FOR UPDATE;

-- name: ListParticipants :many
SELECT * FROM participants WHERE chat_id = @chat_id ORDER BY joined_at, user_id;

-- name: RemoveParticipant :execrows
DELETE FROM participants WHERE chat_id = @chat_id AND user_id = @user_id;

-- name: SetParticipantRole :execrows
UPDATE participants SET role = @role WHERE chat_id = @chat_id AND user_id = @user_id;

-- name: MarkRead :execrows
-- The read marker only moves forward.
UPDATE participants
SET last_read_message_id = GREATEST(COALESCE(last_read_message_id, 0), @message_id::bigint)
WHERE chat_id = @chat_id AND user_id = @user_id;

-- name: LockChatsOfUser :many
SELECT c.id
FROM chats c
JOIN participants p ON p.chat_id = c.id
WHERE p.user_id = @user_id
ORDER BY c.id
FOR UPDATE OF c;

-- name: ListDirectChatIDsOfUser :many
SELECT c.id
FROM chats c
JOIN participants p ON p.chat_id = c.id
WHERE p.user_id = @user_id AND c.type = 'direct'
ORDER BY c.id
FOR UPDATE OF c;

-- name: ListOwnedGroupIDs :many
SELECT c.id
FROM chats c
JOIN participants p ON p.chat_id = c.id
WHERE p.user_id = @user_id AND p.role = 'owner' AND c.type = 'group'
ORDER BY c.id
FOR UPDATE OF c;

-- name: NextOwnerCandidate :one
-- Who inherits a group when its owner leaves: admins first, then moderators, then the longest member.
SELECT user_id
FROM participants
WHERE chat_id = @chat_id AND user_id <> @leaving_user_id
ORDER BY CASE role WHEN 'admin' THEN 0 WHEN 'moderator' THEN 1 ELSE 2 END, joined_at, user_id
LIMIT 1;
