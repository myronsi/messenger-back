# Chats

The chat list, direct chats, pins, read markers and approval requests: `internal/chats` (the use cases) and `internal/httpapi/chat_server.go` (the endpoints). Groups, their members and invitations follow with the group endpoints; messages over REST with the message endpoints.

## The chat list: `GET /chats`

One page of the caller's chats:
- **Order:** pinned chats first (most recently pinned first), then the others by their last activity (the newest message, or the creation of the chat), then by id.
- **Paging:** the cursor is opaque and stable while chats move: a chat that gets a new message while you page jumps to the top and is not shown twice in later pages.
- **Ordering data:** it comes from PostgreSQL alone. `chats.last_message_id` and `chats.last_activity_at` are written with every new message (migration 000009), so the order never needs the message store.

Each entry carries:

- `name` and `avatar_url`: those of the peer as the caller sees them (their contact name for the peer, the peer's privacy settings), or the group's own.
- `peer` (direct chats) and `my_role` (groups; moderators show as `admin`, the contract has no moderators).
- `unread_count` from the Redis counters (`redis.Unread`). When the counters of a user are missing they are rebuilt from ScyllaDB (`CountAfter` from each read marker, at most 999 per chat).
- `last_message`: the newest message the caller can see, read from ScyllaDB in parallel (16 at a time) for the chats of the page. In a direct chat whose last message the caller sent, `read_by` holds the peer once they read it, if they share read receipts. A group's list entry carries no receipts.

`GET /chats/{id}` is one entry (`404` when the caller is not in the chat, `400` for an id that is not a number).

## Direct chats: `POST /chats`

`user_id` and an optional `initial_message`:

1. An existing chat of the pair is returned with `200`. If one of the two blocked the other, the answer is `403 blocked_by_user` instead.
2. Otherwise the other user's `direct_messages` setting decides:
   - `everyone` opens the chat;
   - `shared_chats` opens it only when the two already share a (group) chat, otherwise `403`;
   - `wait_approval` makes a request (`202` with the `ApprovalRequest`). Asking again returns the same pending request.

   A block in either direction is `403`.
3. A new chat is `201`. Both users get `chat_created`, and `initial_message` becomes its first message.

New chats and requests are rate limited (60 per hour, bursts of 20, per user).

`DELETE /chats/{id}` deletes a direct chat for both users, as v1 did:
- both get `chat_deleted`, their unread counters for it are dropped, and the membership cache forgets it;
- `chat.deleted` makes the worker delete the messages;
- the files of v1 attachments are deleted.

Groups are deleted or left with the group endpoints (`422` here).

## Pins and reading

- `PUT`/`DELETE /chats/{id}/pin`. At most 10 pinned chats per user (`409`); pinning twice and unpinning an unpinned chat are fine. The caller's other devices get `chat_list_update`.
- `POST /chats/{id}/read` with `message_id` moves the read marker forward (never back) and answers the unread count that is left. Read receipts go out as for the WebSocket `read` event, and `participants.last_read_at` is the `read_at` of the receipts.

## Approval requests

- `GET /requests` is the caller's inbox of pending requests, newest first, paged by request id. The requester is rendered as the caller sees them, and `preview` is the message they asked with.
- `POST /requests/{id}/approve` opens the direct chat. Its first message is the request's message, sent by the requester. The answer is the chat; both users get `chat_created`.
- `POST /requests/{id}/reject` turns the request down; the requester is not told.
- Answering a request twice is `409`, and someone else's request is `404`. The request is locked while it is answered, so two answers cannot both win.
- The recipient gets `approval_request_created` for a new request.
