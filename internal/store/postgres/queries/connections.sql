-- name: ListUserConnections :many
SELECT * FROM user_connections WHERE user_id = @user_id ORDER BY position, id;

-- name: CountUserConnections :one
SELECT count(*) FROM user_connections WHERE user_id = @user_id;

-- name: FindUserConnection :one
SELECT * FROM user_connections WHERE user_id = @user_id AND provider = @provider AND external_id = @external_id;

-- name: InsertUserConnection :one
INSERT INTO user_connections (user_id, provider, external_id, display_name, profile_url, position)
VALUES (@user_id, @provider, @external_id, @display_name, sqlc.narg(profile_url),
        COALESCE((SELECT max(position) + 1 FROM user_connections WHERE user_id = @user_id), 0))
RETURNING *;

-- name: RefreshUserConnection :one
UPDATE user_connections
SET display_name = @display_name, profile_url = sqlc.narg(profile_url), verified_at = now()
WHERE id = @id
RETURNING *;

-- name: DeleteUserConnection :execrows
DELETE FROM user_connections WHERE id = @id AND user_id = @user_id;

-- name: SetUserConnectionPosition :execrows
UPDATE user_connections SET position = @position WHERE id = @id AND user_id = @user_id;
