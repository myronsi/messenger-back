-- name: CreateUser :one
INSERT INTO users (username, display_name, password_hash, terms_version, terms_accepted_at, age_confirmed_at)
VALUES (@username, @display_name, @password_hash, sqlc.narg(terms_version), sqlc.narg(terms_accepted_at), sqlc.narg(age_confirmed_at))
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
    bio = CASE WHEN @set_bio::bool THEN sqlc.narg(bio) ELSE bio END,
    pronouns = CASE WHEN @set_pronouns::bool THEN sqlc.narg(pronouns) ELSE pronouns END,
    birthday_day = CASE WHEN @set_birthday::bool THEN sqlc.narg(birthday_day)::smallint ELSE birthday_day END,
    birthday_month = CASE WHEN @set_birthday::bool THEN sqlc.narg(birthday_month)::smallint ELSE birthday_month END,
    birthday_year = CASE WHEN @set_birthday::bool THEN sqlc.narg(birthday_year)::smallint ELSE birthday_year END,
    time_zone = COALESCE(sqlc.narg(time_zone), time_zone)
WHERE id = @id
RETURNING *;

-- name: SetUserMusic :one
UPDATE users
SET music_attachment_id = sqlc.narg(attachment_id), music_title = sqlc.narg(title), music_artist = sqlc.narg(artist)
WHERE id = @id
RETURNING *;

-- name: SetUserBackground :one
UPDATE users
SET background_kind = sqlc.narg(kind), background_attachment_id = sqlc.narg(attachment_id),
    background_preset = sqlc.narg(preset), background_colors = sqlc.narg(colors)::text[]
WHERE id = @id
RETURNING *;

-- name: ListBirthdayCandidates :many
SELECT r.id AS receiver_id, u.id AS user_id, u.display_name, c.id AS chat_id,
       date_part('year', timezone(r.time_zone, sqlc.arg(now)::timestamptz))::smallint AS local_year
FROM users r
LEFT JOIN notification_preferences n ON n.user_id = r.id
JOIN participants pr ON pr.user_id = r.id
JOIN chats c ON c.id = pr.chat_id AND c.type = 'direct'
JOIN participants pu ON pu.chat_id = c.id AND pu.user_id <> r.id
JOIN users u ON u.id = pu.user_id
WHERE COALESCE(n.enabled, true) AND COALESCE(n.birthday_reminders, true)
  AND u.birthday_month = ANY(sqlc.arg(months)::smallint[])
  AND u.birthday_day = ANY(sqlc.arg(days)::smallint[])
  AND date_part('hour', timezone(r.time_zone, sqlc.arg(now)::timestamptz)) >= sqlc.arg(from_hour)::int
  AND u.birthday_month = date_part('month', timezone(r.time_zone, sqlc.arg(now)::timestamptz))::smallint
  AND (u.birthday_day = date_part('day', timezone(r.time_zone, sqlc.arg(now)::timestamptz))::smallint
       OR (u.birthday_month = 2 AND u.birthday_day = 29 AND date_part('day', timezone(r.time_zone, sqlc.arg(now)::timestamptz)) = 28
           AND date_part('day', date_trunc('month', timezone(r.time_zone, sqlc.arg(now)::timestamptz)) + interval '1 month - 1 day') = 28))
  AND NOT EXISTS (SELECT 1 FROM birthday_reminders_sent s
                  WHERE s.user_id = u.id AND s.receiver_id = r.id AND s.year = date_part('year', timezone(r.time_zone, sqlc.arg(now)::timestamptz))::smallint)
  AND NOT EXISTS (SELECT 1 FROM user_blocks b
                  WHERE (b.blocker_id = u.id AND b.blocked_id = r.id) OR (b.blocker_id = r.id AND b.blocked_id = u.id))
  AND NOT pr.is_request AND NOT pu.is_request
  AND NOT EXISTS (SELECT 1 FROM notification_mutes m
                  WHERE m.user_id = r.id AND m.chat_id = c.id AND m.until_at > now())
ORDER BY r.id, u.id
LIMIT sqlc.arg(max_rows);

-- name: MarkBirthdayReminder :execrows
INSERT INTO birthday_reminders_sent (user_id, receiver_id, year) VALUES (@user_id, @receiver_id, @year)
ON CONFLICT DO NOTHING;

-- name: DeleteSessionsOfUser :many
DELETE FROM user_sessions WHERE user_id = @user_id RETURNING id;
