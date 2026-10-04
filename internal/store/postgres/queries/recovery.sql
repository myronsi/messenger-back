-- name: CreateRecoveryToken :exec
INSERT INTO recovery_tokens (jti_hash, user_id, expires_at) VALUES (@jti_hash, @user_id, @expires_at);

-- name: ConsumeRecoveryToken :one
-- Single use: the first caller gets the user, everyone else (and expired tokens) get no row.
UPDATE recovery_tokens SET used_at = now()
WHERE jti_hash = @jti_hash AND used_at IS NULL AND expires_at > now()
RETURNING user_id;

-- name: DeleteStaleRecoveryTokens :execrows
DELETE FROM recovery_tokens WHERE expires_at < now() - interval '1 day';