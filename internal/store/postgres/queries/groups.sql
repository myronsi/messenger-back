-- name: UpdateGroupInfo :one
-- Changes only what is given: the name, and the description when set_description (null clears it).
UPDATE chats
SET name = COALESCE(sqlc.narg(name), name),
    description = CASE WHEN @set_description::bool THEN COALESCE(sqlc.narg(description), '') ELSE description END
WHERE id = @id AND type = 'group'
RETURNING *;

-- name: CreateGroupInvite :one
-- A pending invitation of the same requester, recipient and group is reused.
INSERT INTO approval_requests (type, requester_id, recipient_id, chat_id)
VALUES ('group_invite', @requester_id, @recipient_id, @chat_id)
ON CONFLICT (requester_id, recipient_id, chat_id) WHERE status = 'pending' AND type = 'group_invite'
DO UPDATE SET created_at = approval_requests.created_at
RETURNING *, (xmax = 0) AS created;

-- name: PendingInviteRecipients :many
-- Users invited to the group who did not answer yet.
SELECT DISTINCT recipient_id FROM approval_requests
WHERE chat_id = @chat_id AND type = 'group_invite' AND status = 'pending';
