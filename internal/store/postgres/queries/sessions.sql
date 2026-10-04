-- name: CreateSession :one
INSERT INTO user_sessions (user_id, refresh_token_hash, user_agent, ip_address, expires_at)
VALUES (@user_id, @refresh_token_hash, @user_agent, @ip_address, @expires_at)
RETURNING *;

-- name: GetSession :one
SELECT * FROM user_sessions WHERE id = @id;

-- name: GetSessionByRefreshHash :one
SELECT * FROM user_sessions WHERE refresh_token_hash = @hash;

-- name: LockSessionByRefreshHash :one
SELECT * FROM user_sessions WHERE refresh_token_hash = @hash FOR UPDATE;

-- name: GetRotatedToken :one
SELECT session_id, rotated_at FROM user_session_rotated_tokens WHERE token_hash = @hash;

-- name: LockSession :one
SELECT * FROM user_sessions WHERE id = @id FOR UPDATE;

-- name: RecordRotatedToken :exec
INSERT INTO user_session_rotated_tokens (token_hash, session_id) VALUES (@token_hash, @session_id);

-- name: RotateSession :one
UPDATE user_sessions
SET refresh_token_hash = @new_hash,
    last_active_at     = now(),
    expires_at         = @expires_at,
    user_agent         = COALESCE(sqlc.narg('user_agent')::text, user_agent),
    ip_address         = COALESCE(sqlc.narg('ip_address')::inet, ip_address)
WHERE id = @id
RETURNING *;

-- name: ListActiveSessions :many
SELECT * FROM user_sessions
WHERE user_id = @user_id AND revoked_at IS NULL AND expires_at > now()
ORDER BY last_active_at DESC, created_at DESC;

-- name: TouchSession :execrows
UPDATE user_sessions SET last_active_at = now() WHERE id = @id AND revoked_at IS NULL;

-- name: RevokeSession :many
UPDATE user_sessions SET revoked_at = now()
WHERE id = @id AND user_id = @user_id AND revoked_at IS NULL
RETURNING id;

-- name: RevokeSessionByID :many
UPDATE user_sessions SET revoked_at = now() WHERE id = @id AND revoked_at IS NULL RETURNING id;

-- name: RevokeOtherSessions :many
UPDATE user_sessions SET revoked_at = now()
WHERE user_id = @user_id AND id <> @keep_id AND revoked_at IS NULL
RETURNING id;

-- name: RevokeAllSessions :many
UPDATE user_sessions SET revoked_at = now() WHERE user_id = @user_id AND revoked_at IS NULL RETURNING id;
-- name: DeleteEndedSessions :execrows
-- Deleting a session cascades to its rotated refresh hashes.
DELETE FROM user_sessions WHERE id IN (
    SELECT id FROM user_sessions
    WHERE expires_at < sqlc.arg(cutoff)::timestamptz OR revoked_at < sqlc.arg(cutoff)::timestamptz
    LIMIT sqlc.arg(batch)::int
);

-- name: DeleteOldRotatedTokens :execrows
DELETE FROM user_session_rotated_tokens WHERE token_hash IN (
    SELECT token_hash FROM user_session_rotated_tokens
    WHERE rotated_at < sqlc.arg(cutoff)::timestamptz
    LIMIT sqlc.arg(batch)::int
);
