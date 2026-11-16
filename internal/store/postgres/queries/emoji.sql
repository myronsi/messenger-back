-- name: CreateEmojiPack :one
INSERT INTO emoji_packs (owner_id, title, slug) VALUES (@owner_id, @title, @slug)
RETURNING *;

-- name: EnsureGroupEmojiPack :exec
INSERT INTO emoji_packs (chat_id, title) VALUES (@chat_id, @title)
ON CONFLICT (chat_id) DO NOTHING;

-- name: GetGroupEmojiPack :one
SELECT * FROM emoji_packs WHERE chat_id = @chat_id AND deleted_at IS NULL;

-- name: GetEmojiPack :one
SELECT * FROM emoji_packs WHERE id = @id;

-- name: LockEmojiPack :one
SELECT * FROM emoji_packs WHERE id = @id FOR UPDATE;

-- name: GetEmojiPackBySlug :one
SELECT * FROM emoji_packs WHERE slug = @slug AND deleted_at IS NULL;

-- name: RenameEmojiPack :execrows
UPDATE emoji_packs SET title = @title, version = version + 1 WHERE id = @id AND deleted_at IS NULL;

-- name: DeleteEmojiPack :execrows
UPDATE emoji_packs SET deleted_at = now(), version = version + 1 WHERE id = @id AND deleted_at IS NULL;

-- name: ListUserEmojiPacks :many
SELECT p.*, EXISTS (SELECT 1 FROM user_emoji_packs u WHERE u.pack_id = p.id AND u.user_id = @user_id)::bool AS installed
FROM emoji_packs p
WHERE p.deleted_at IS NULL
  AND (p.owner_id = @user_id
       OR (p.hidden_at IS NULL AND EXISTS (SELECT 1 FROM user_emoji_packs u WHERE u.pack_id = p.id AND u.user_id = @user_id))
       OR (p.chat_id IS NOT NULL AND EXISTS (SELECT 1 FROM participants pa WHERE pa.chat_id = p.chat_id AND pa.user_id = @user_id)))
ORDER BY p.chat_id NULLS FIRST, p.id
LIMIT @max_rows;

-- name: IsEmojiPackInstalled :one
SELECT EXISTS (SELECT 1 FROM user_emoji_packs WHERE user_id = @user_id AND pack_id = @pack_id)::bool;

-- name: InstallEmojiPack :exec
INSERT INTO user_emoji_packs (user_id, pack_id) VALUES (@user_id, @pack_id) ON CONFLICT DO NOTHING;

-- name: UninstallEmojiPack :exec
DELETE FROM user_emoji_packs WHERE user_id = @user_id AND pack_id = @pack_id;

-- name: ListPackEmoji :many
SELECT * FROM custom_emoji WHERE pack_id = ANY(@pack_ids::bigint[]) AND deleted_at IS NULL ORDER BY pack_id, position, id;

-- name: CountPackEmoji :one
SELECT count(*)::int FROM custom_emoji WHERE pack_id = @pack_id AND deleted_at IS NULL;

-- name: AddCustomEmoji :one
INSERT INTO custom_emoji (pack_id, attachment_id, shortcode, is_animated, position)
VALUES (@pack_id, @attachment_id, @shortcode, @is_animated,
        (SELECT COALESCE(max(position) + 1, 0) FROM custom_emoji WHERE pack_id = @pack_id))
RETURNING *;

-- name: RenameCustomEmoji :one
UPDATE custom_emoji SET shortcode = @shortcode WHERE id = @id AND pack_id = @pack_id AND deleted_at IS NULL
RETURNING *;

-- name: DeleteCustomEmoji :execrows
UPDATE custom_emoji SET deleted_at = now() WHERE id = @id AND pack_id = @pack_id AND deleted_at IS NULL;

-- name: GetCustomEmoji :one
SELECT e.*, p.deleted_at AS pack_deleted_at, p.slug AS pack_slug, a.storage_key, a.mime_type
FROM custom_emoji e
JOIN emoji_packs p ON p.id = e.pack_id
JOIN attachments a ON a.id = e.attachment_id
WHERE e.id = @id;

-- name: ListUsableEmoji :many
SELECT e.id, e.shortcode, e.is_animated
FROM custom_emoji e
JOIN emoji_packs p ON p.id = e.pack_id
WHERE e.id = ANY(@ids::bigint[])
  AND e.deleted_at IS NULL AND p.deleted_at IS NULL
  AND (p.owner_id = @user_id
       OR (p.hidden_at IS NULL AND EXISTS (SELECT 1 FROM user_emoji_packs u WHERE u.pack_id = p.id AND u.user_id = @user_id))
       OR (p.chat_id IS NOT NULL AND EXISTS (SELECT 1 FROM participants pa WHERE pa.chat_id = p.chat_id AND pa.user_id = @user_id)));

-- name: InsertEmojiPackReport :execrows
INSERT INTO emoji_pack_reports (pack_id, reporter_id, reason) VALUES (@pack_id, @reporter_id, @reason)
ON CONFLICT DO NOTHING;

-- name: CountRecentEmojiReports :one
SELECT count(*)::int FROM emoji_pack_reports WHERE reporter_id = @reporter_id AND created_at > @since;

-- name: HideReportedEmojiPack :execrows
UPDATE emoji_packs SET hidden_at = now(), version = version + 1
WHERE id = @id AND hidden_at IS NULL
  AND (SELECT count(*) FROM emoji_pack_reports r WHERE r.pack_id = @id) >= @threshold::int;

-- name: IsEmojiImage :one
SELECT EXISTS (SELECT 1 FROM custom_emoji WHERE attachment_id = @attachment_id);
