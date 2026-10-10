-- name: TouchChatActivity :exec
-- A new message: the chat moves to the top of its members' lists. Never moves back for an older id.
UPDATE chats
SET last_message_id = @message_id::bigint, last_activity_at = @at
WHERE id = @chat_id AND (last_message_id IS NULL OR last_message_id < @message_id::bigint);

-- name: ListChatEntries :many
-- A user's chats: pinned ones first (most recently pinned first), then by last activity, then by id. The
-- cursor is the last entry of the previous page: (pinned, sort time, chat id).
SELECT c.*, p.role, p.last_read_message_id, pin.pinned_at,
       COALESCE(pin.pinned_at, c.last_activity_at)::timestamptz AS sort_at
FROM participants p
JOIN chats c ON c.id = p.chat_id
LEFT JOIN user_chat_pins pin ON pin.user_id = p.user_id AND pin.chat_id = p.chat_id
WHERE p.user_id = @user_id
  AND (
    sqlc.narg(after_chat_id)::bigint IS NULL
    OR (pin.pinned_at IS NULL)::int > sqlc.narg(after_unpinned)::int
    OR ((pin.pinned_at IS NULL)::int = sqlc.narg(after_unpinned)::int AND (
          COALESCE(pin.pinned_at, c.last_activity_at) < sqlc.narg(after_sort_at)::timestamptz
          OR (COALESCE(pin.pinned_at, c.last_activity_at) = sqlc.narg(after_sort_at)::timestamptz AND c.id < sqlc.narg(after_chat_id)::bigint)))
  )
ORDER BY (pin.pinned_at IS NULL), COALESCE(pin.pinned_at, c.last_activity_at) DESC, c.id DESC
LIMIT @max_rows;

-- name: GetChatEntry :one
SELECT c.*, p.role, p.last_read_message_id, pin.pinned_at,
       COALESCE(pin.pinned_at, c.last_activity_at)::timestamptz AS sort_at
FROM participants p
JOIN chats c ON c.id = p.chat_id
LEFT JOIN user_chat_pins pin ON pin.user_id = p.user_id AND pin.chat_id = p.chat_id
WHERE p.user_id = @user_id AND p.chat_id = @chat_id;

-- name: ListOtherParticipants :many
-- The other members of the chats (the peers of direct chats), with their read markers.
SELECT chat_id, user_id, last_read_message_id, last_read_at
FROM participants
WHERE chat_id = ANY(@chat_ids::bigint[]) AND user_id <> @user_id
ORDER BY chat_id, user_id;

-- name: CountPins :one
-- Pins of chats the user is still in (pins of chats they left do not use up the limit).
SELECT count(*) FROM user_chat_pins pin
JOIN participants p ON p.chat_id = pin.chat_id AND p.user_id = pin.user_id
WHERE pin.user_id = @user_id;

-- name: PinChat :execrows
INSERT INTO user_chat_pins (user_id, chat_id) VALUES (@user_id, @chat_id) ON CONFLICT DO NOTHING;

-- name: UnpinChat :execrows
DELETE FROM user_chat_pins WHERE user_id = @user_id AND chat_id = @chat_id;

-- name: CreateDirectRequest :one
-- A pending request of the same pair is reused (the unique index allows one), so asking twice is harmless.
INSERT INTO approval_requests (type, requester_id, recipient_id, message_text)
VALUES ('direct_message', @requester_id, @recipient_id, sqlc.narg(message_text))
ON CONFLICT (requester_id, recipient_id) WHERE status = 'pending' AND type = 'direct_message'
DO UPDATE SET message_text = COALESCE(approval_requests.message_text, EXCLUDED.message_text)
RETURNING *, (xmax = 0) AS created;

-- name: GetApprovalRequest :one
SELECT * FROM approval_requests WHERE id = @id;

-- name: LockApprovalRequest :one
SELECT * FROM approval_requests WHERE id = @id FOR UPDATE;

-- name: ListPendingRequests :many
-- The recipient's inbox of direct-message requests, newest first; the cursor is the last request id. (Group
-- invitations are answered with the group endpoints.)
SELECT * FROM approval_requests
WHERE recipient_id = @recipient_id AND status = 'pending' AND type = 'direct_message'
  AND (sqlc.narg(before_id)::bigint IS NULL OR id < sqlc.narg(before_id)::bigint)
ORDER BY id DESC
LIMIT @max_rows;

-- name: RespondToRequest :execrows
UPDATE approval_requests SET status = @status, responded_at = now() WHERE id = @id AND status = 'pending';

-- name: LockDirectPair :exec
-- Serializes everything that opens a direct chat of a pair or asks for one (key: the direct_key), so a
-- request cannot be made while the chat is being opened.
SELECT pg_advisory_xact_lock(hashtextextended(@direct_key::text, 0));

-- name: CloseDirectRequests :many
-- The pair has a chat now: pending direct-message requests between them, in either direction, are done.
UPDATE approval_requests
SET status = 'approved', responded_at = now()
WHERE type = 'direct_message' AND status = 'pending'
  AND ((requester_id = @a AND recipient_id = @b) OR (requester_id = @b AND recipient_id = @a))
RETURNING *;
