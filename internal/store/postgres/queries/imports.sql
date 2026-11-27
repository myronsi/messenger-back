-- name: InsertChatImport :one
INSERT INTO chat_imports (chat_id, imported_by, source, state, message_count, file_count, first_at, last_at, lower_bound, first_message_id, last_message_id)
VALUES (@chat_id, @imported_by, @source, 'running', @message_count, @file_count, @first_at, @last_at, @lower_bound, @first_message_id, @last_message_id)
RETURNING *;

-- name: InsertImportSender :exec
INSERT INTO chat_import_senders (import_id, sender_key, name, message_count, assigned_user_id, state)
VALUES (@import_id, @sender_key, @name, @message_count, sqlc.narg('assigned_user_id'), @state);

-- name: InsertImportWindow :exec
INSERT INTO chat_import_windows (import_id, chunk, node, start_ms, size)
VALUES (@import_id, @chunk, @node, @start_ms, @size);

-- name: GetChatImport :one
SELECT * FROM chat_imports WHERE id = @id;

-- name: ChatImportsOfChat :many
SELECT * FROM chat_imports WHERE chat_id = @chat_id ORDER BY first_message_id;

-- name: ChatImportsByID :many
SELECT * FROM chat_imports WHERE id = ANY(@ids::bigint[]);

-- name: ImportSendersOf :many
SELECT * FROM chat_import_senders WHERE import_id = ANY(@import_ids::bigint[]) ORDER BY import_id, sender_key;

-- name: ImportWindowsOf :many
SELECT import_id, chunk, node, start_ms, size FROM chat_import_windows WHERE import_id = @import_id ORDER BY chunk;

-- name: AdvanceChatImport :execrows
UPDATE chat_imports SET next_index = @next_index, last_activity_at = now()
WHERE id = @id AND state = 'running' AND next_index = @from_index;

-- name: RunningChatImportsOfUser :many
SELECT * FROM chat_imports WHERE imported_by = @user_id AND state = 'running' ORDER BY id;

-- name: IdleChatImports :many
SELECT * FROM chat_imports WHERE state = 'running' AND last_activity_at < @before ORDER BY last_activity_at LIMIT @max_rows;

-- name: FinishChatImport :one
UPDATE chat_imports SET state = 'done', finished_at = now()
WHERE id = @id AND state = 'running' AND next_index = message_count
RETURNING *;

-- name: MarkChatImportRemoving :one
UPDATE chat_imports SET state = 'removing'
WHERE id = @id AND state IN ('running', 'done')
RETURNING *;

-- name: DeleteChatImport :exec
DELETE FROM chat_imports WHERE id = @id;

-- name: AssignImportSender :one
UPDATE chat_import_senders
SET assigned_user_id = sqlc.narg('user_id'),
    state = CASE WHEN sqlc.narg('user_id')::bigint IS NULL THEN 'unassigned' ELSE 'pending' END,
    decided_at = NULL
WHERE import_id = @import_id AND sender_key = @sender_key
  AND state IN ('unassigned', 'pending', 'declined')
  AND (sqlc.narg('user_id')::bigint IS NULL OR NOT (sqlc.narg('user_id')::bigint = ANY(declined_user_ids)))
RETURNING *;

-- name: ConfirmImportSender :one
UPDATE chat_import_senders SET state = 'confirmed', decided_at = now()
WHERE import_id = @import_id AND sender_key = @sender_key AND assigned_user_id = @user_id AND state = 'pending'
RETURNING *;

-- name: DeclineImportSender :one
UPDATE chat_import_senders
SET state = 'declined', assigned_user_id = NULL, declined_user_ids = array_append(declined_user_ids, @user_id::bigint), decided_at = now()
WHERE import_id = @import_id AND sender_key = @sender_key AND assigned_user_id = @user_id AND state = 'pending'
RETURNING *;

-- name: WithdrawImportSender :one
UPDATE chat_import_senders
SET state = 'unassigned', assigned_user_id = NULL, declined_user_ids = array_append(declined_user_ids, @user_id::bigint), decided_at = now()
WHERE import_id = @import_id AND sender_key = @sender_key AND assigned_user_id = @user_id AND state = 'confirmed'
RETURNING *;

-- name: ImportLowerBound :one
SELECT GREATEST(
    COALESCE((SELECT cleared_before_message_id FROM chats WHERE id = @chat_id), 0),
    COALESCE((SELECT max(cleared_before_message_id) FROM participants WHERE chat_id = @chat_id), 0)
)::bigint AS bound;

-- name: AdvanceReadPointers :exec
UPDATE participants SET last_read_message_id = @to_id
WHERE chat_id = @chat_id AND (last_read_message_id IS NULL OR (last_read_message_id >= @from_id AND last_read_message_id < @to_id));

-- name: UnlinkImportedAttachments :exec
DELETE FROM attachment_links WHERE chat_id = @chat_id AND message_id BETWEEN @from_id::bigint AND @to_id::bigint;

-- name: DeleteImportedURLs :exec
DELETE FROM chat_links WHERE chat_id = @chat_id AND message_id BETWEEN @from_id::bigint AND @to_id::bigint;

-- name: DeleteImportedSharedPins :exec
DELETE FROM message_pins WHERE chat_id = @chat_id AND message_id BETWEEN @from_id::bigint AND @to_id::bigint;

-- name: DeleteImportedPersonalPins :exec
DELETE FROM message_pins_personal WHERE chat_id = @chat_id AND message_id BETWEEN @from_id::bigint AND @to_id::bigint;
