-- name: InsertCall :one
INSERT INTO calls (chat_id, kind, initiator_id, state, max_participants, ring_expires_at)
VALUES (@chat_id, @kind, @initiator_id, 'ringing', @max_participants, @ring_expires_at)
RETURNING *;

-- name: InsertCallParticipants :exec
INSERT INTO call_participants (call_id, user_id, position, state)
SELECT @call_id::bigint, unnest(@user_ids::bigint[]), unnest(@positions::int[]), unnest(@states::text[]);

-- name: GetCall :one
SELECT * FROM calls WHERE id = @id;

-- name: LockCall :one
SELECT * FROM calls WHERE id = @id FOR UPDATE;

-- name: ShareCall :one
SELECT * FROM calls WHERE id = @id FOR SHARE;

-- name: OpenCallInChat :one
SELECT * FROM calls WHERE chat_id = @chat_id AND state <> 'ended';

-- name: ListCallParticipants :many
SELECT * FROM call_participants WHERE call_id = @call_id ORDER BY position, user_id;

-- name: ListParticipantsOfCalls :many
SELECT * FROM call_participants WHERE call_id = ANY(@call_ids::bigint[]) ORDER BY call_id, position, user_id;

-- name: UpdateCall :exec
UPDATE calls
SET state       = @state,
    end_reason  = sqlc.narg(end_reason),
    epoch       = @epoch,
    version     = @version,
    started_at  = sqlc.narg(started_at),
    ended_at    = sqlc.narg(ended_at),
    empty_since = sqlc.narg(empty_since)
WHERE id = @id;

-- name: UpsertCallParticipant :exec
INSERT INTO call_participants (call_id, user_id, position, state, session_id, identity_key, joined_at, left_at, disconnected_at)
VALUES (@call_id, @user_id, @position, @state, sqlc.narg(session_id), sqlc.narg(identity_key), sqlc.narg(joined_at), sqlc.narg(left_at), sqlc.narg(disconnected_at))
ON CONFLICT (call_id, user_id) DO UPDATE
SET position        = EXCLUDED.position,
    state           = EXCLUDED.state,
    session_id      = EXCLUDED.session_id,
    identity_key    = EXCLUDED.identity_key,
    joined_at       = EXCLUDED.joined_at,
    left_at         = EXCLUDED.left_at,
    disconnected_at = EXCLUDED.disconnected_at;

-- name: DeleteCallEnvelopes :exec
DELETE FROM call_key_envelopes WHERE call_id = @call_id;

-- name: DeleteCallEnvelopesBefore :exec
DELETE FROM call_key_envelopes WHERE call_id = @call_id AND epoch < @epoch;

-- name: GetCallStartRequest :one
SELECT * FROM call_start_requests
WHERE user_id = @user_id AND client_request_id = @client_request_id AND created_at > now() - interval '24 hours';

-- name: SaveCallStartRequest :exec
INSERT INTO call_start_requests (user_id, client_request_id, call_id, created)
VALUES (@user_id, @client_request_id, @call_id, @created)
ON CONFLICT (user_id, client_request_id) DO UPDATE
SET call_id = EXCLUDED.call_id, created = EXCLUDED.created, created_at = now();

-- name: ListUserCalls :many
SELECT * FROM calls
WHERE id IN (
    SELECT c.id FROM calls c
    JOIN call_participants p ON p.call_id = c.id AND p.user_id = @user_id
    WHERE c.state <> 'ended' AND c.chat_id IS NOT NULL
      AND (p.state IN ('ringing', 'joined') OR (c.initiator_id = @user_id AND p.joined_at IS NULL))
      AND EXISTS (SELECT 1 FROM participants m WHERE m.chat_id = c.chat_id AND m.user_id = @user_id)
)
ORDER BY created_at DESC, id DESC
LIMIT 50;

-- name: JoinedCallOfUser :one
SELECT call_id FROM call_participants WHERE user_id = @user_id AND state = 'joined';

-- name: JoinedCallsOfSessions :many
SELECT DISTINCT call_id FROM call_participants WHERE state = 'joined' AND session_id = ANY(@session_ids::uuid[]);

-- name: CountJoinedParticipants :one
SELECT count(*) FROM call_participants WHERE state = 'joined';

-- name: DueCalls :many
SELECT c.id FROM calls c
WHERE c.state <> 'ended' AND (
    c.chat_id IS NULL
    OR c.created_at <= @stale_before
    OR (c.ring_expires_at <= @now AND (c.started_at IS NULL
        OR EXISTS (SELECT 1 FROM call_participants p WHERE p.call_id = c.id AND p.state = 'ringing')))
    OR (c.empty_since IS NOT NULL AND c.empty_since <= @grace_before)
    OR EXISTS (SELECT 1 FROM call_participants p
               WHERE p.call_id = c.id AND p.state = 'joined' AND p.disconnected_at <= @grace_before)
)
ORDER BY c.id
LIMIT @max_rows;

-- name: OpenCallIDs :many
SELECT id FROM calls WHERE state <> 'ended' ORDER BY id LIMIT @max_rows;

-- name: DeleteOrphanedCalls :execrows
DELETE FROM calls WHERE chat_id IS NULL AND state = 'ended';

-- name: ClaimCallSummary :one
UPDATE calls
SET summary_claimed_at = now(), summary_message_id = COALESCE(summary_message_id, @message_id::bigint)
WHERE id = @id AND state = 'ended' AND chat_id IS NOT NULL AND summary_posted_at IS NULL
  AND (summary_claimed_at IS NULL OR summary_claimed_at < now() - interval '1 minute')
RETURNING summary_message_id::bigint;

-- name: ReleaseCallSummary :exec
UPDATE calls SET summary_claimed_at = NULL WHERE id = @id AND summary_posted_at IS NULL;

-- name: FinishCallSummary :exec
UPDATE calls SET summary_posted_at = now(), summary_claimed_at = NULL WHERE id = @id;

-- name: PendingCallSummaries :many
SELECT id FROM calls
WHERE state = 'ended' AND chat_id IS NOT NULL AND summary_posted_at IS NULL AND ended_at > @since AND ended_at <= @until
ORDER BY ended_at
LIMIT @max_rows;

-- name: PurgeEndedCalls :execrows
DELETE FROM calls WHERE state = 'ended' AND ended_at < @before;

-- name: PurgeCallStartRequests :execrows
DELETE FROM call_start_requests WHERE created_at < @before;

-- name: PurgeEndedCallEnvelopes :execrows
DELETE FROM call_key_envelopes e USING calls c WHERE c.id = e.call_id AND c.state = 'ended';

-- name: PurgeCallIdentities :execrows
DELETE FROM call_identities i USING user_sessions s
WHERE s.id = i.session_id AND (s.revoked_at IS NOT NULL OR s.expires_at <= now());

-- name: GetCallIdentity :one
SELECT public_key FROM call_identities WHERE session_id = @session_id;

-- name: ShareCallIdentity :one
SELECT public_key FROM call_identities WHERE session_id = @session_id AND user_id = @user_id FOR SHARE;

-- name: ShareChatMember :one
SELECT user_id FROM participants WHERE chat_id = @chat_id AND user_id = @user_id FOR KEY SHARE;

-- name: ListCallCandidates :many
SELECT p.user_id,
       COALESCE(s.calls_enabled, true)::bool AS calls_enabled,
       EXISTS (SELECT 1 FROM user_blocks b
               WHERE (b.blocker_id = @initiator_id AND b.blocked_id = p.user_id)
                  OR (b.blocker_id = p.user_id AND b.blocked_id = @initiator_id))::bool AS blocked
FROM participants p
LEFT JOIN user_privacy_settings s ON s.user_id = p.user_id
WHERE p.chat_id = @chat_id
ORDER BY p.joined_at, p.user_id
FOR KEY SHARE OF p;

-- name: BlockedAmong :one
SELECT EXISTS (
    SELECT 1 FROM user_blocks
    WHERE (blocker_id = @user_id AND blocked_id = ANY(@others::bigint[]))
       OR (blocked_id = @user_id AND blocker_id = ANY(@others::bigint[]))
)::bool;

-- name: LockCallIdentity :one
SELECT * FROM call_identities WHERE session_id = @session_id FOR UPDATE;

-- name: UpsertCallIdentity :execrows
INSERT INTO call_identities (session_id, user_id, public_key)
SELECT s.id, s.user_id, @public_key::text FROM user_sessions s
WHERE s.id = @session_id AND s.user_id = @user_id AND s.revoked_at IS NULL AND s.expires_at > now()
ON CONFLICT (session_id) DO UPDATE SET public_key = EXCLUDED.public_key, updated_at = now();

-- name: DeleteCallIdentities :exec
DELETE FROM call_identities WHERE session_id = ANY(@session_ids::uuid[]);

-- name: SaveCallEnvelopes :exec
INSERT INTO call_key_envelopes (call_id, epoch, recipient_session_id, sender_session_id, sender_user_id, enc, ciphertext)
SELECT @call_id::bigint, @epoch::bigint, unnest(@recipients::uuid[]), @sender_session_id::uuid, @sender_user_id::bigint,
       unnest(@encs::text[]), unnest(@ciphertexts::text[])
ON CONFLICT (call_id, epoch, recipient_session_id, sender_session_id) DO UPDATE
SET enc = EXCLUDED.enc, ciphertext = EXCLUDED.ciphertext, sender_user_id = EXCLUDED.sender_user_id, created_at = now();

-- name: ListCallEnvelopes :many
SELECT * FROM call_key_envelopes
WHERE call_id = @call_id AND epoch = @epoch AND recipient_session_id = @recipient_session_id
ORDER BY sender_session_id;

-- name: ListCallPushDevices :many
SELECT d.id, d.user_id, d.destination, d.p256dh, d.auth, d.session_id,
       COALESCE(n.enabled, true)::bool AS enabled, COALESCE(n.previews, false)::bool AS previews
FROM notification_devices d
JOIN user_sessions s ON s.id = d.session_id
LEFT JOIN notification_preferences n ON n.user_id = d.user_id
WHERE d.user_id = ANY(@user_ids::bigint[]) AND d.platform = 'web' AND s.revoked_at IS NULL AND s.expires_at > now();
