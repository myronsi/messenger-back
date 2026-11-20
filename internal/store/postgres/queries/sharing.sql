-- name: GetPublicName :one
SELECT * FROM public_names WHERE lower_name = LOWER(@name::text);

-- name: CountChatMembers :one
SELECT count(*)::int FROM participants WHERE chat_id = @chat_id;

-- name: SetChatPublicName :one
UPDATE chats SET public_name = sqlc.narg(public_name) WHERE id = @id AND type = 'group'
RETURNING *;

-- name: SearchPublicGroups :many
SELECT c.*, (SELECT count(*) FROM participants x WHERE x.chat_id = c.id)::int AS member_count
FROM chats c
WHERE c.public_name IS NOT NULL
  AND (LOWER(c.public_name) LIKE LOWER(@prefix::text) || '%' OR LOWER(c.name) LIKE '%' || LOWER(@contains::text) || '%')
  AND (sqlc.narg(after_id)::bigint IS NULL OR c.id > sqlc.narg(after_id)::bigint)
ORDER BY c.id
LIMIT @max_rows;

-- name: CreateInviteLink :one
INSERT INTO chat_invite_links (chat_id, token, name, created_by, expires_at, max_uses)
VALUES (@chat_id, @token, sqlc.narg(name), @created_by, sqlc.narg(expires_at), sqlc.narg(max_uses))
RETURNING *;

-- name: ListInviteLinks :many
SELECT * FROM chat_invite_links WHERE chat_id = @chat_id ORDER BY id DESC LIMIT 500;

-- name: CountActiveInviteLinks :one
SELECT count(*)::int FROM chat_invite_links
WHERE chat_id = @chat_id AND revoked_at IS NULL
  AND (expires_at IS NULL OR expires_at > now())
  AND (max_uses IS NULL OR use_count < max_uses);

-- name: GetInviteLink :one
SELECT * FROM chat_invite_links WHERE id = @id AND chat_id = @chat_id;

-- name: UpdateInviteLink :one
UPDATE chat_invite_links
SET name = CASE WHEN @set_name::bool THEN sqlc.narg(name) ELSE name END,
    expires_at = CASE WHEN @set_expires_at::bool THEN sqlc.narg(expires_at) ELSE expires_at END,
    max_uses = CASE WHEN @set_max_uses::bool THEN sqlc.narg(max_uses) ELSE max_uses END
WHERE id = @id AND chat_id = @chat_id
RETURNING *;

-- name: RevokeInviteLink :execrows
UPDATE chat_invite_links SET revoked_at = COALESCE(revoked_at, now()) WHERE id = @id AND chat_id = @chat_id;

-- name: GetActiveInviteLink :one
SELECT * FROM chat_invite_links
WHERE token = @token AND revoked_at IS NULL
  AND (expires_at IS NULL OR expires_at > now())
  AND (max_uses IS NULL OR use_count < max_uses);

-- name: UseInviteLink :one
UPDATE chat_invite_links SET use_count = use_count + 1
WHERE id = @id AND revoked_at IS NULL
  AND (expires_at IS NULL OR expires_at > now())
  AND (max_uses IS NULL OR use_count < max_uses)
RETURNING *;

-- name: BanFromChat :exec
INSERT INTO chat_bans (chat_id, user_id, banned_by) VALUES (@chat_id, @user_id, @banned_by)
ON CONFLICT (chat_id, user_id) DO NOTHING;

-- name: UnbanFromChat :execrows
DELETE FROM chat_bans WHERE chat_id = @chat_id AND user_id = @user_id;

-- name: IsBannedFromChat :one
SELECT EXISTS (SELECT 1 FROM chat_bans WHERE chat_id = @chat_id AND user_id = @user_id);

-- name: ListChatBans :many
SELECT * FROM chat_bans
WHERE chat_id = @chat_id
  AND (sqlc.narg(after_created_at)::timestamptz IS NULL
       OR (created_at, user_id) < (sqlc.narg(after_created_at)::timestamptz, sqlc.narg(after_user_id)::bigint))
ORDER BY created_at DESC, user_id DESC
LIMIT @max_rows;

-- name: AddUnreadMentions :exec
INSERT INTO unread_mentions (user_id, chat_id, message_id)
SELECT p.user_id, p.chat_id, @message_id::bigint
FROM participants p
WHERE p.chat_id = @chat_id AND p.user_id = ANY(@user_ids::BIGINT[])
  AND COALESCE(p.last_read_message_id, 0) < @message_id::bigint
ON CONFLICT DO NOTHING;

-- name: RemoveUnreadMentionsOfMessages :exec
DELETE FROM unread_mentions WHERE chat_id = @chat_id AND message_id = ANY(@message_ids::BIGINT[]);

-- name: RemoveUnreadMentionsExcept :exec
DELETE FROM unread_mentions
WHERE chat_id = @chat_id AND message_id = @message_id AND NOT (user_id = ANY(@keep::BIGINT[]));

-- name: ListUnreadMentions :many
SELECT m.message_id FROM unread_mentions m
JOIN participants p ON p.chat_id = m.chat_id AND p.user_id = m.user_id
WHERE m.user_id = @user_id AND m.chat_id = @chat_id AND m.message_id > COALESCE(p.last_read_message_id, 0)
ORDER BY m.message_id
LIMIT 100;

-- name: CountUnreadMentions :many
SELECT m.chat_id, count(*)::int AS mentions
FROM unread_mentions m
JOIN participants p ON p.chat_id = m.chat_id AND p.user_id = m.user_id
WHERE m.user_id = @user_id AND m.chat_id = ANY(@chat_ids::BIGINT[]) AND m.message_id > COALESCE(p.last_read_message_id, 0)
GROUP BY m.chat_id;

-- name: GroupMembersByUsername :many
SELECT p.user_id, LOWER(u.username)::text AS lower_username
FROM participants p JOIN users u ON u.id = p.user_id
WHERE p.chat_id = @chat_id AND LOWER(u.username) = ANY(@lower_usernames::TEXT[]);

-- name: ListGroupMemberIDs :many
SELECT user_id FROM participants WHERE chat_id = @chat_id;
