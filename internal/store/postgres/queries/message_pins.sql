-- name: GetSharedPin :one
SELECT * FROM message_pins WHERE chat_id = @chat_id AND message_id = @message_id;

-- name: CountSharedPins :one
SELECT count(*)::int FROM message_pins WHERE chat_id = @chat_id;

-- name: InsertSharedPin :one
INSERT INTO message_pins (chat_id, message_id, pinned_by)
VALUES (@chat_id, @message_id, @pinned_by)
RETURNING *;

-- name: DeleteSharedPin :one
DELETE FROM message_pins WHERE chat_id = @chat_id AND message_id = @message_id
RETURNING *;

-- name: GetPersonalPin :one
SELECT * FROM message_pins_personal WHERE user_id = @user_id AND chat_id = @chat_id AND message_id = @message_id;

-- name: CountPersonalPins :one
SELECT count(*)::int FROM message_pins_personal WHERE user_id = @user_id AND chat_id = @chat_id;

-- name: InsertPersonalPin :one
INSERT INTO message_pins_personal (user_id, chat_id, message_id)
VALUES (@user_id, @chat_id, @message_id)
RETURNING *;

-- name: DeletePersonalPin :execrows
DELETE FROM message_pins_personal WHERE user_id = @user_id AND chat_id = @chat_id AND message_id = @message_id;

-- name: InsertPinHide :execrows
INSERT INTO message_pin_hides (user_id, chat_id, message_id)
VALUES (@user_id, @chat_id, @message_id)
ON CONFLICT DO NOTHING;

-- name: DeletePinHide :execrows
DELETE FROM message_pin_hides WHERE user_id = @user_id AND chat_id = @chat_id AND message_id = @message_id;

-- name: ListVisiblePins :many
WITH floor AS (
    SELECT GREATEST(COALESCE(p.cleared_before_message_id, 0), COALESCE(c.cleared_before_message_id, 0))::bigint AS bound
    FROM participants p
    JOIN chats c ON c.id = p.chat_id
    WHERE p.chat_id = @chat_id AND p.user_id = @user_id
), visible AS (
    SELECT mp.message_id, 'everyone'::text AS scope, mp.pinned_by, mp.pinned_at
    FROM message_pins mp
    WHERE mp.chat_id = @chat_id
      AND NOT EXISTS (
          SELECT 1 FROM message_pin_hides h
          WHERE h.user_id = @user_id AND h.chat_id = mp.chat_id AND h.message_id = mp.message_id
      )
    UNION ALL
    SELECT pp.message_id, 'me'::text AS scope, pp.user_id AS pinned_by, pp.pinned_at
    FROM message_pins_personal pp
    WHERE pp.user_id = @user_id AND pp.chat_id = @chat_id
      AND NOT EXISTS (SELECT 1 FROM message_pins mp WHERE mp.chat_id = pp.chat_id AND mp.message_id = pp.message_id)
)
SELECT v.message_id::bigint AS message_id, v.scope::text AS scope, v.pinned_by::bigint AS pinned_by,
       v.pinned_at::timestamptz AS pinned_at
FROM visible v, floor f
WHERE v.message_id > f.bound
  AND (NOT @has_cursor::boolean OR (v.pinned_at, v.message_id) < (@after_at::timestamptz, @after_id::bigint))
ORDER BY v.pinned_at DESC, v.message_id DESC
LIMIT @row_limit;

-- name: CountVisiblePins :many
WITH floors AS (
    SELECT p.chat_id, GREATEST(COALESCE(p.cleared_before_message_id, 0), COALESCE(c.cleared_before_message_id, 0))::bigint AS bound
    FROM participants p
    JOIN chats c ON c.id = p.chat_id
    WHERE p.user_id = @user_id AND p.chat_id = ANY(@chat_ids::bigint[])
), visible AS (
    SELECT mp.chat_id
    FROM message_pins mp
    JOIN floors f ON f.chat_id = mp.chat_id
    WHERE mp.message_id > f.bound
      AND NOT EXISTS (
          SELECT 1 FROM message_pin_hides h
          WHERE h.user_id = @user_id AND h.chat_id = mp.chat_id AND h.message_id = mp.message_id
      )
    UNION ALL
    SELECT pp.chat_id
    FROM message_pins_personal pp
    JOIN floors f ON f.chat_id = pp.chat_id
    WHERE pp.user_id = @user_id AND pp.message_id > f.bound
      AND NOT EXISTS (SELECT 1 FROM message_pins mp WHERE mp.chat_id = pp.chat_id AND mp.message_id = pp.message_id)
)
SELECT v.chat_id::bigint AS chat_id, count(*)::int AS pins
FROM visible v
GROUP BY v.chat_id;

-- name: DeleteSharedPinsOf :many
DELETE FROM message_pins WHERE chat_id = @chat_id AND message_id = ANY(@message_ids::bigint[])
RETURNING message_id;

-- name: DeletePersonalPinsOf :execrows
DELETE FROM message_pins_personal WHERE chat_id = @chat_id AND message_id = ANY(@message_ids::bigint[]);

-- name: DeleteUserPersonalPinsOf :execrows
DELETE FROM message_pins_personal
WHERE user_id = @user_id AND chat_id = @chat_id AND message_id = ANY(@message_ids::bigint[]);

-- name: HideSharedPinsOf :execrows
INSERT INTO message_pin_hides (user_id, chat_id, message_id)
SELECT @user_id::bigint, mp.chat_id, mp.message_id
FROM message_pins mp
WHERE mp.chat_id = @chat_id AND mp.message_id = ANY(@message_ids::bigint[])
ON CONFLICT DO NOTHING;

-- name: DeleteSharedPinsUpTo :execrows
DELETE FROM message_pins WHERE chat_id = @chat_id AND message_id <= @up_to;

-- name: DeletePersonalPinsUpTo :execrows
DELETE FROM message_pins_personal WHERE chat_id = @chat_id AND message_id <= @up_to;

-- name: DeleteUserPersonalPinsUpTo :execrows
DELETE FROM message_pins_personal WHERE user_id = @user_id AND chat_id = @chat_id AND message_id <= @up_to;

-- name: DeleteUserChatPersonalPins :execrows
DELETE FROM message_pins_personal WHERE user_id = @user_id AND chat_id = @chat_id;

-- name: DeleteUserChatPinHides :execrows
DELETE FROM message_pin_hides WHERE user_id = @user_id AND chat_id = @chat_id;
