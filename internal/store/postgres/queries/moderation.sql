-- name: UpsertUserReport :one
INSERT INTO user_reports (reporter_id, target_id, reason, note, context, weight, snapshot, created_at, updated_at)
VALUES (@reporter_id, @target_id, @reason, sqlc.narg(note), @context, @weight::float8, @snapshot, @now, @now)
ON CONFLICT (reporter_id, target_id) DO UPDATE SET reason = EXCLUDED.reason, note = EXCLUDED.note, updated_at = EXCLUDED.updated_at, withdrawn_at = NULL
RETURNING (xmax = 0)::boolean AS inserted, weight::float8 AS weight;

-- name: WithdrawUserReport :execrows
UPDATE user_reports SET withdrawn_at = @now::timestamptz, updated_at = @now::timestamptz
WHERE reporter_id = @reporter_id AND target_id = @target_id AND withdrawn_at IS NULL;

-- name: CountUserReportsByReporterSince :one
SELECT count(*) FROM user_reports
WHERE reporter_id = @reporter_id AND target_id <> @except_target AND updated_at >= @since;

-- name: CountReportedUsersSince :one
SELECT count(*) FROM user_reports
WHERE reporter_id = @reporter_id AND target_id <> @except_target AND created_at >= @since;

-- name: CountUnconnectedReportsSince :one
SELECT count(*) FROM user_reports
WHERE target_id = @target_id AND reporter_id <> @except_reporter AND context = 'none' AND created_at >= @since;

-- name: ZeroUnconnectedReportsSince :execrows
UPDATE user_reports SET weight = 0
WHERE target_id = @target_id AND context = 'none' AND created_at >= @since AND weight > 0;

-- name: ListReportFacts :many
SELECT reporter_id, weight::float8 AS weight, created_at FROM user_reports
WHERE target_id = @target_id AND created_at >= @since AND withdrawn_at IS NULL AND dismissed_at IS NULL
ORDER BY created_at;

-- name: DeleteUserReportsBefore :execrows
DELETE FROM user_reports WHERE created_at < @before;

-- name: TargetInitiatedContact :one
SELECT EXISTS (
    SELECT 1 FROM chats c
    JOIN participants pr ON pr.chat_id = c.id AND pr.user_id = @reporter_id
    JOIN participants pt ON pt.chat_id = c.id AND pt.user_id = @target_id
    WHERE c.created_by = @target_id
    UNION ALL
    SELECT 1 FROM approval_requests a WHERE a.requester_id = @target_id AND a.recipient_id = @reporter_id
    UNION ALL
    SELECT 1 FROM calls k
    JOIN participants pr ON pr.chat_id = k.chat_id AND pr.user_id = @reporter_id
    WHERE k.initiator_id = @target_id
);

-- name: ShareAnyChat :one
SELECT EXISTS (
    SELECT 1 FROM participants pr
    JOIN participants pt ON pt.chat_id = pr.chat_id AND pt.user_id = @target_id
    WHERE pr.user_id = @reporter_id
);

-- name: CountDirectChatsCreatedSince :one
SELECT count(*) FROM chats WHERE type = 'direct' AND created_by = @user_id AND created_at >= @since
  AND direct_key <> @self_key::text;

-- name: EnsureUserModeration :exec
INSERT INTO user_moderation (user_id, status_since) VALUES (@user_id, 'epoch') ON CONFLICT (user_id) DO NOTHING;

-- name: LockUserModeration :one
SELECT user_id, status, score::float8 AS score, status_since, probation_until, appeal_blocked_until, last_appeal_at,
       decided_by, decided_at, reason_code, reason_text
FROM user_moderation WHERE user_id = @user_id FOR UPDATE;

-- name: GetUserModeration :one
SELECT user_id, status, score::float8 AS score, status_since, probation_until, appeal_blocked_until, last_appeal_at,
       decided_by, decided_at, reason_code, reason_text
FROM user_moderation WHERE user_id = @user_id;

-- name: UpdateUserModeration :exec
UPDATE user_moderation
SET status = @status, score = @score::float8, status_since = @status_since, probation_until = sqlc.narg(probation_until),
    appeal_blocked_until = sqlc.narg(appeal_blocked_until), last_appeal_at = sqlc.narg(last_appeal_at),
    decided_by = @decided_by, decided_at = sqlc.narg(decided_at), reason_code = sqlc.narg(reason_code), reason_text = sqlc.narg(reason_text)
WHERE user_id = @user_id;

-- name: InsertUserModerationEvent :exec
INSERT INTO user_moderation_events (user_id, from_status, to_status, score, cause, reason_code, reason_text, staff_ref, created_at)
VALUES (@user_id, @from_status, @to_status, @score::float8, @cause, sqlc.narg(reason_code), sqlc.narg(reason_text), sqlc.narg(staff_ref), @now);

-- name: ListUserModerationEvents :many
SELECT id, user_id, from_status, to_status, score::float8 AS score, cause, reason_code, reason_text, staff_ref, created_at
FROM user_moderation_events WHERE user_id = @user_id ORDER BY id;

-- name: ListModerationStatuses :many
SELECT user_id, status FROM user_moderation WHERE user_id = ANY(@user_ids::BIGINT[]) AND status <> 'good';

-- name: ListModerationDue :many
SELECT user_id FROM user_moderation
WHERE (status <> 'good' OR probation_until IS NOT NULL) AND user_id > @after
ORDER BY user_id
LIMIT @max_rows;

-- name: DismissUserReports :many
UPDATE user_reports SET dismissed_at = @now::timestamptz
WHERE target_id = @target_id AND withdrawn_at IS NULL AND dismissed_at IS NULL
  AND (sqlc.narg(reporter_ids)::BIGINT[] IS NULL OR reporter_id = ANY(sqlc.narg(reporter_ids)::BIGINT[]))
RETURNING reporter_id;
