-- name: InsertPoll :exec
INSERT INTO polls (message_id, chat_id, creator_id, question, options, multiple_answers, anonymous, correct_option, explanation, close_at)
VALUES (@message_id, @chat_id, @creator_id, @question, @options, @multiple_answers, @anonymous, sqlc.narg('correct_option'), sqlc.narg('explanation'), sqlc.narg('close_at'))
ON CONFLICT (message_id) DO NOTHING;

-- name: GetPolls :many
SELECT * FROM polls WHERE message_id = ANY(@message_ids::bigint[]);

-- name: LockPoll :one
SELECT * FROM polls WHERE message_id = @message_id FOR UPDATE;

-- name: PollCounts :many
SELECT message_id, option, count(*)::bigint AS votes
FROM poll_votes WHERE message_id = ANY(@message_ids::bigint[])
GROUP BY message_id, option;

-- name: PollVoterCounts :many
SELECT message_id, count(DISTINCT user_id)::bigint AS voters
FROM poll_votes WHERE message_id = ANY(@message_ids::bigint[])
GROUP BY message_id;

-- name: UserPollVotes :many
SELECT message_id, option FROM poll_votes
WHERE message_id = ANY(@message_ids::bigint[]) AND user_id = @user_id
ORDER BY message_id, option;

-- name: AllPollVotes :many
SELECT user_id, option FROM poll_votes WHERE message_id = @message_id ORDER BY user_id, option;

-- name: DeleteUserPollVotes :execrows
DELETE FROM poll_votes WHERE message_id = @message_id AND user_id = @user_id;

-- name: InsertPollVotes :exec
INSERT INTO poll_votes (message_id, user_id, option, voted_at)
SELECT @message_id, @user_id, unnest(@options::smallint[]), @voted_at;

-- name: BumpPollVersion :one
UPDATE polls SET version = version + 1 WHERE message_id = @message_id RETURNING *;

-- name: ClosePoll :one
UPDATE polls SET closed_at = @closed_at, closed_by = sqlc.narg('closed_by'), version = version + 1
WHERE message_id = @message_id AND closed_at IS NULL
RETURNING *;

-- name: CloseDuePolls :many
UPDATE polls SET closed_at = close_at, version = version + 1
WHERE message_id IN (
    SELECT p.message_id FROM polls p
    WHERE p.closed_at IS NULL AND p.close_at IS NOT NULL AND p.close_at <= @now
    ORDER BY p.close_at
    LIMIT @max_rows
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: PollVoters :many
SELECT user_id, voted_at FROM poll_votes
WHERE message_id = @message_id AND option = @option
  AND (sqlc.narg('after_voted_at')::timestamptz IS NULL
       OR (voted_at, user_id) < (sqlc.narg('after_voted_at')::timestamptz, sqlc.narg('after_user_id')::bigint))
ORDER BY voted_at DESC, user_id DESC
LIMIT @max_rows;

-- name: DeletePolls :exec
DELETE FROM polls WHERE chat_id = @chat_id AND message_id = ANY(@message_ids::bigint[]);

-- name: DeletePollsUpTo :exec
DELETE FROM polls WHERE chat_id = @chat_id AND message_id <= @up_to;
