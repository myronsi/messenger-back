-- name: DeleteMessageURLs :exec
DELETE FROM chat_links WHERE chat_id = @chat_id AND message_id = @message_id;

-- name: InsertMessageURL :exec
INSERT INTO chat_links (chat_id, message_id, url, created_at) VALUES (@chat_id, @message_id, @url, @created_at)
ON CONFLICT DO NOTHING;

-- name: DeleteURLsUpTo :exec
DELETE FROM chat_links WHERE chat_id = @chat_id AND message_id <= @message_id;

-- name: ListLinkMessageIDs :many
SELECT DISTINCT message_id
FROM chat_links
WHERE chat_id = @chat_id AND (sqlc.narg(before)::bigint IS NULL OR message_id < sqlc.narg(before)::bigint)
ORDER BY message_id DESC
LIMIT @max_rows;

-- name: ListGroupMembersPage :many
SELECT p.user_id, p.role, p.joined_at,
       (CASE p.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 WHEN 'moderator' THEN 2 ELSE 3 END)::int AS role_rank,
       LOWER(u.display_name)::text AS sort_name
FROM participants p
JOIN users u ON u.id = p.user_id
WHERE p.chat_id = @chat_id
  AND (sqlc.narg(q)::text IS NULL OR LOWER(u.username) LIKE sqlc.narg(q)::text || '%' OR LOWER(u.display_name) LIKE '%' || sqlc.narg(q)::text || '%')
  AND (sqlc.narg(after_rank)::int IS NULL OR
       ((CASE p.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 WHEN 'moderator' THEN 2 ELSE 3 END), LOWER(u.display_name), p.user_id)
         > (sqlc.narg(after_rank)::int, sqlc.narg(after_name)::text, sqlc.narg(after_id)::bigint))
ORDER BY role_rank, sort_name, p.user_id
LIMIT @max_rows;

-- name: CountGroupMembers :one
SELECT count(*)
FROM participants p
JOIN users u ON u.id = p.user_id
WHERE p.chat_id = @chat_id
  AND (sqlc.narg(q)::text IS NULL OR LOWER(u.username) LIKE sqlc.narg(q)::text || '%' OR LOWER(u.display_name) LIKE '%' || sqlc.narg(q)::text || '%');

-- name: ListCommonGroups :many
SELECT c.id, c.name, c.avatar_attachment_id, c.last_activity_at,
       (SELECT count(*) FROM participants x WHERE x.chat_id = c.id)::int AS member_count
FROM chats c
JOIN participants a ON a.chat_id = c.id AND a.user_id = @viewer_id
JOIN participants b ON b.chat_id = c.id AND b.user_id = @user_id
WHERE c.type = 'group'
  AND (sqlc.narg(after_at)::timestamptz IS NULL OR (c.last_activity_at, c.id) < (sqlc.narg(after_at)::timestamptz, sqlc.narg(after_id)::bigint))
ORDER BY c.last_activity_at DESC, c.id DESC
LIMIT @max_rows;
