-- name: UpsertUnsentMessage :exec
INSERT INTO unsent_messages (user_id, chat_id, client_temp_id, type, content, reply_to, attachment_ids, drawings, sticker_id, error_code)
VALUES (@user_id, @chat_id, @client_temp_id, @type, sqlc.narg(content), sqlc.narg(reply_to), @attachment_ids, sqlc.narg(drawings),
        sqlc.narg(sticker_id), @error_code)
ON CONFLICT (user_id, chat_id, client_temp_id) DO UPDATE SET error_code = EXCLUDED.error_code;

-- name: TrimUnsentMessages :exec
-- Keeps the newest rows of a user.
DELETE FROM unsent_messages u
WHERE u.user_id = @user_id
  AND (u.chat_id, u.client_temp_id) NOT IN (
    SELECT k.chat_id, k.client_temp_id FROM unsent_messages k WHERE k.user_id = @user_id ORDER BY k.created_at DESC LIMIT @keep::int);

-- name: ListUnsentMessages :many
SELECT * FROM unsent_messages WHERE user_id = @user_id AND chat_id = @chat_id ORDER BY created_at, client_temp_id;

-- name: DeleteUnsentMessage :execrows
DELETE FROM unsent_messages WHERE user_id = @user_id AND chat_id = @chat_id AND client_temp_id = @client_temp_id;
