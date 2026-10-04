-- name: RecordSecurityEvent :one
INSERT INTO user_security_events (user_id, event_type, ip_address, user_agent, details)
VALUES (@user_id, @event_type, @ip_address, @user_agent, @details)
RETURNING *;

-- name: ListSecurityEvents :many
-- Newest first; pass the id of the last row of the previous page as before_id.
SELECT *
FROM user_security_events
WHERE user_id = @user_id AND (sqlc.narg('before_id')::bigint IS NULL OR id < sqlc.narg('before_id')::bigint)
ORDER BY id DESC
LIMIT @max_rows;
