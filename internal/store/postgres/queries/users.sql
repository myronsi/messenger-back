-- name: CreateUser :one
INSERT INTO users (username, display_name, password_hash)
VALUES (@username, @display_name, @password_hash)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = @id;

-- name: LockUser :one
SELECT * FROM users WHERE id = @id FOR UPDATE;

-- name: LockUserShared :one
-- Takes the lock a foreign key check would take, but up front, so operations that reference a user lock
-- the user before any chat, the same order DeleteAccount uses.
SELECT id FROM users WHERE id = @id FOR KEY SHARE;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE LOWER(username) = LOWER(@username);

-- name: UpdateUserProfile :one
UPDATE users
SET display_name = @display_name, bio = @bio, avatar_url = @avatar_url
WHERE id = @id
RETURNING *;

-- name: SetPasswordHash :execrows
UPDATE users SET password_hash = @password_hash WHERE id = @id;

-- name: TouchLastSeen :execrows
UPDATE users SET last_seen_at = now() WHERE id = @id;

-- name: DeleteUser :execrows
DELETE FROM users WHERE id = @id;

-- name: RehashPassword :execrows
-- Only replaces the hash that was verified, so a password change in between is never overwritten.
UPDATE users SET password_hash = @new_hash WHERE id = @id AND password_hash = @old_hash;

-- name: EnsurePrivacySettings :exec
INSERT INTO user_privacy_settings (user_id) VALUES (@user_id) ON CONFLICT (user_id) DO NOTHING;


-- name: ListUsersByIDs :many
SELECT * FROM users WHERE id = ANY(@ids::BIGINT[]);

-- name: PatchUserProfile :one
-- Changes only what is given, so concurrent edits of different fields do not undo each other.
UPDATE users
SET display_name = COALESCE(sqlc.narg(display_name), display_name),
    bio = CASE WHEN @set_bio::bool THEN sqlc.narg(bio) ELSE bio END
WHERE id = @id
RETURNING *;

-- name: DeleteSessionsOfUser :many
DELETE FROM user_sessions WHERE user_id = @user_id RETURNING id;
