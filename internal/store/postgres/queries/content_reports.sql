-- name: UpsertContentReport :one
INSERT INTO content_reports (reporter_id, kind, chat_id, message_id, target_id, reason, note, content, created_at, updated_at)
VALUES (@reporter_id, @kind, @chat_id, @message_id, sqlc.narg(target_id), @reason, sqlc.narg(note), @content, @now, @now)
ON CONFLICT (reporter_id, chat_id, message_id) DO UPDATE SET reason = EXCLUDED.reason, note = EXCLUDED.note, updated_at = EXCLUDED.updated_at, withdrawn_at = NULL
RETURNING id, (xmax = 0)::boolean AS inserted;

-- name: WithdrawContentReport :execrows
UPDATE content_reports SET withdrawn_at = @now::timestamptz, updated_at = @now::timestamptz
WHERE reporter_id = @reporter_id AND chat_id = @chat_id AND message_id = @message_id AND withdrawn_at IS NULL;

-- name: CountReportsBesideContentReport :one
SELECT ((SELECT count(*) FROM user_reports u WHERE u.reporter_id = @reporter_id AND u.updated_at >= @since)
     + (SELECT count(*) FROM content_reports c WHERE c.reporter_id = @reporter_id AND c.updated_at >= @since
          AND NOT (c.chat_id = @chat_id AND c.message_id = @message_id)))::bigint AS total;

-- name: GetContentReport :one
SELECT id, reporter_id, kind, chat_id, message_id, target_id, reason, note, content, created_at, updated_at, withdrawn_at
FROM content_reports WHERE reporter_id = @reporter_id AND chat_id = @chat_id AND message_id = @message_id;

-- name: DeleteContentReportsBefore :execrows
DELETE FROM content_reports WHERE created_at < @before;
