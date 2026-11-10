-- name: UpsertBuiltinStickerPack :one
INSERT INTO sticker_packs (builtin_key, title, is_default)
VALUES (@builtin_key, @title, @is_default)
ON CONFLICT (builtin_key) DO UPDATE SET title = EXCLUDED.title, is_default = EXCLUDED.is_default, deleted_at = NULL
RETURNING id;

-- name: ListBuiltinStickerKeys :many
SELECT id, builtin_key::text AS builtin_key FROM stickers WHERE pack_id = @pack_id AND builtin_key IS NOT NULL;

-- name: InsertBuiltinSticker :exec
INSERT INTO stickers (pack_id, attachment_id, builtin_key, emoji, position, is_animated)
VALUES (@pack_id, @attachment_id, @builtin_key, @emoji, @position, @is_animated)
ON CONFLICT (pack_id, builtin_key) DO NOTHING;

-- name: UpdateBuiltinSticker :exec
UPDATE stickers SET emoji = @emoji, position = @position, is_animated = @is_animated, deleted_at = NULL WHERE id = @id;

-- name: RetireBuiltinStickers :exec
UPDATE stickers SET deleted_at = now()
WHERE pack_id = @pack_id AND builtin_key IS NOT NULL AND deleted_at IS NULL AND NOT (builtin_key = ANY(@keep::text[]));

-- name: GetStickerPack :one
SELECT * FROM sticker_packs WHERE id = @id;

-- name: GetStickerPackInstallation :one
SELECT installed FROM user_sticker_packs WHERE user_id = @user_id AND pack_id = @pack_id;

-- name: ListInstalledStickerPacks :many
SELECT p.*
FROM sticker_packs p
LEFT JOIN user_sticker_packs u ON u.pack_id = p.id AND u.user_id = @user_id
WHERE p.deleted_at IS NULL AND (p.hidden_at IS NULL OR p.owner_id = @user_id)
  AND (u.installed OR (u.installed IS NULL AND p.is_default))
ORDER BY (u.installed IS NULL), u.changed_at DESC, p.id
LIMIT @max_rows;

-- name: SetStickerPackInstalled :exec
INSERT INTO user_sticker_packs (user_id, pack_id, installed) VALUES (@user_id, @pack_id, @installed)
ON CONFLICT (user_id, pack_id) DO UPDATE SET installed = EXCLUDED.installed, changed_at = now();

-- name: ListStickersByID :many
SELECT sqlc.embed(s), p.deleted_at AS pack_deleted_at, p.hidden_at AS pack_hidden_at,
       p.slug AS pack_slug, p.owner_id AS pack_owner_id, sqlc.embed(a)
FROM stickers s
JOIN sticker_packs p ON p.id = s.pack_id
JOIN attachments a ON a.id = s.attachment_id
WHERE s.id = ANY(@ids::bigint[]);

-- name: ListPackStickers :many
SELECT sqlc.embed(s), p.slug AS pack_slug, p.owner_id AS pack_owner_id, sqlc.embed(a)
FROM stickers s
JOIN sticker_packs p ON p.id = s.pack_id
JOIN attachments a ON a.id = s.attachment_id
WHERE s.pack_id = ANY(@pack_ids::bigint[]) AND s.deleted_at IS NULL
ORDER BY s.pack_id, s.position, s.id;

-- name: TouchRecentSticker :exec
INSERT INTO user_recent_stickers (user_id, sticker_id) VALUES (@user_id, @sticker_id)
ON CONFLICT (user_id, sticker_id) DO UPDATE SET used_at = now();

-- name: TrimRecentStickers :exec
DELETE FROM user_recent_stickers r
WHERE r.user_id = @user_id AND r.sticker_id NOT IN (
    SELECT k.sticker_id FROM user_recent_stickers k WHERE k.user_id = @user_id ORDER BY k.used_at DESC, k.sticker_id DESC LIMIT @keep
);

-- name: ListRecentStickers :many
SELECT sqlc.embed(s), p.slug AS pack_slug, p.owner_id AS pack_owner_id, sqlc.embed(a)
FROM user_recent_stickers r
JOIN stickers s ON s.id = r.sticker_id
JOIN sticker_packs p ON p.id = s.pack_id
JOIN attachments a ON a.id = s.attachment_id
WHERE r.user_id = @user_id AND s.deleted_at IS NULL AND p.deleted_at IS NULL AND p.hidden_at IS NULL
ORDER BY r.used_at DESC, r.sticker_id DESC
LIMIT @max_rows;

-- name: RememberStickerPack :exec
INSERT INTO user_sticker_packs (user_id, pack_id, installed) VALUES (@user_id, @pack_id, false)
ON CONFLICT (user_id, pack_id) DO NOTHING;

-- name: LockStickerPackOwner :one
SELECT id FROM users WHERE id = @id FOR NO KEY UPDATE;

-- name: CountOwnedStickerPacks :one
SELECT count(*)::int FROM sticker_packs WHERE owner_id = @owner_id AND deleted_at IS NULL;

-- name: InsertOwnedStickerPack :one
INSERT INTO sticker_packs (owner_id, title, slug) VALUES (@owner_id, @title, @slug) RETURNING *;

-- name: GetStickerPackBySlug :one
SELECT * FROM sticker_packs WHERE slug = @slug;

-- name: ListOwnedStickerPacks :many
SELECT * FROM sticker_packs WHERE owner_id = @owner_id AND deleted_at IS NULL ORDER BY id DESC LIMIT @max_rows;

-- name: RenameStickerPack :one
UPDATE sticker_packs SET title = @title WHERE id = @id AND deleted_at IS NULL RETURNING *;

-- name: DeleteStickerPack :exec
UPDATE sticker_packs SET deleted_at = now() WHERE id = @id AND deleted_at IS NULL;

-- name: DeleteStickerPacksOfOwner :exec
UPDATE sticker_packs SET deleted_at = now() WHERE owner_id = @owner_id AND deleted_at IS NULL;

-- name: LockStickerPack :one
SELECT * FROM sticker_packs WHERE id = @id FOR NO KEY UPDATE;

-- name: PackStickerStats :one
SELECT count(*)::int AS live, coalesce(max(position) + 1, 0)::int AS next_position
FROM stickers WHERE pack_id = @pack_id AND deleted_at IS NULL;

-- name: InsertOwnedSticker :one
INSERT INTO stickers (pack_id, attachment_id, emoji, position) VALUES (@pack_id, @attachment_id, @emoji, @position)
RETURNING id;

-- name: SetStickerEmoji :execrows
UPDATE stickers SET emoji = @emoji WHERE id = @id AND pack_id = @pack_id AND deleted_at IS NULL;

-- name: DeleteSticker :execrows
UPDATE stickers SET deleted_at = now() WHERE id = @id AND pack_id = @pack_id AND deleted_at IS NULL;

-- name: ListLiveStickerIDs :many
SELECT id FROM stickers WHERE pack_id = @pack_id AND deleted_at IS NULL;

-- name: SetStickerPositions :exec
UPDATE stickers s SET position = (o.ord - 1)::int
FROM unnest(@ids::bigint[]) WITH ORDINALITY AS o(id, ord)
WHERE s.id = o.id AND s.pack_id = @pack_id;

-- name: CountStickerReportsSince :one
SELECT count(*)::int FROM sticker_pack_reports WHERE reporter_id = @reporter_id AND created_at > @since;

-- name: InsertStickerPackReport :execrows
INSERT INTO sticker_pack_reports (pack_id, reporter_id, reason) VALUES (@pack_id, @reporter_id, @reason)
ON CONFLICT (pack_id, reporter_id) DO NOTHING;

-- name: HideReportedStickerPack :execrows
UPDATE sticker_packs SET hidden_at = now()
WHERE id = @id AND hidden_at IS NULL
  AND (SELECT count(*) FROM sticker_pack_reports r WHERE r.pack_id = @id) >= @threshold::int;

-- name: UnhideStickerPack :execrows
UPDATE sticker_packs SET hidden_at = NULL WHERE id = @id AND hidden_at IS NOT NULL;

-- name: ClearStickerPackReports :exec
DELETE FROM sticker_pack_reports WHERE pack_id = @pack_id;
