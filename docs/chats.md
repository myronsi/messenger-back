# Chats

The chat list, direct chats, groups, pins, read markers and approval requests: `internal/chats` (the use cases) and `internal/httpapi/chat_server.go`, `group_server.go` (the endpoints). Messages over REST are in [messages.md](messages.md).

## The chat list: `GET /chats`

One page of the caller's chats:
- **Order:** pinned chats first (most recently pinned first), then the others by their last activity (the newest message, or the creation of the chat), then by id.
- **Paging:** the cursor is opaque and names the last entry of the page. The list is a moving target: a chat that moves above the cursor while you page (a new message, a pin) is not in the later pages, and one that moves below it (unpinned) can show up twice. Clients page once after connecting and then follow the realtime events, which keep the list current.
- **Ordering data:** it comes from PostgreSQL alone. `chats.last_message_id` and `chats.last_activity_at` are written with every new message (migration 000009), so the order never needs the message store.
- **v1 chats:** chats from before migration 000009 order by their creation time and show no `last_message` until their next message. `cmd/migrate-v1` (#54) sets both columns from the migrated history.

Each entry carries:

- `name` and `avatar_url`: those of the peer as the caller sees them (their contact name for the peer, the peer's privacy settings), or the group's own.
- `peer` (direct chats) and `my_role` (groups; moderators show as `admin`, the contract has no moderators).
- `unread_count` from the Redis counters (`redis.Unread`), at most 999. When a user's counters are missing, a list request rebuilds them from ScyllaDB: `CountAfter` from each read marker, 16 chats at a time, one rebuild per user even for concurrent requests. A single chat (`GET /chats/{id}`, the events) counts only itself.
- `last_message`: the newest message the caller can see, read from ScyllaDB in parallel (16 at a time) for the chats of the page. In a direct chat whose last message the caller sent, `read_by` holds the peer once they read it, if they share read receipts and did not block the caller. A group's list entry carries no receipts.

`GET /chats/{id}` is one entry (`404` when the caller is not in the chat, `400` for an id that is not a number).

## Direct chats: `POST /chats`

`user_id` and an optional `initial_message`:

1. An existing chat of the pair is returned with `200`. If one of the two blocked the other, the answer is `403 blocked_by_user` instead.
2. Otherwise the other user's `direct_messages` setting decides:
   - `everyone` opens the chat;
   - `shared_chats` opens it only when the two already share a (group) chat, otherwise `403`;
   - `wait_approval` makes a request (`202` with the `ApprovalRequest`, the requester as they see themselves). Asking again returns the same pending request; asking after the chat was opened meanwhile returns the chat.

   A block in either direction is `403`.
3. A new chat is `201`. Both users get `chat_created` (before any message of the chat), and `initial_message` becomes its first message.

Everything that opens a direct chat or asks for one takes a lock on the pair (`pg_advisory_xact_lock` on the direct key), and opening a chat closes every pending request of the pair in either direction and sends their messages. So two users who ask each other end up with one chat that holds both messages, and an approval racing a new request leaves no stale request behind.

New chats and requests are rate limited (60 per hour, bursts of 20, per user).

`DELETE /chats/{id}` deletes a direct chat for both users, as v1 did:
- both get `chat_deleted`, their unread counters for it are dropped, and the membership cache forgets it;
- `chat.deleted` makes the worker delete the messages;
- the files of v1 attachments are deleted.

Groups are deleted or left with the group endpoints (`422` here).

## Pins and reading

- `PUT`/`DELETE /chats/{id}/pin`. At most 10 pinned chats per user, counting only chats the user is still in (`409`); pinning twice and unpinning an unpinned chat are fine. The caller's devices get `chat_list_update`. New messages and reads do not send `chat_list_update`: `message_new` and `read` carry what a client needs to reorder its list and update the counts.
- `POST /chats/{id}/read` with `message_id` moves the read marker forward (never back) and answers the unread count that is left. Read receipts go out as for the WebSocket `read` event, and `participants.last_read_at` is the `read_at` of the receipts.

## Approval requests

- `GET /requests` is the caller's inbox of pending requests (direct messages and group invitations), newest first, paged by request id (a cursor that is not one of these is `400`). The requester is rendered as the caller sees them; `preview` is the message a direct-message request asked with, `group_name` the group of an invitation.
- `POST /requests/{id}/approve` on a group invitation adds the caller to the group (members get `group_updated`, the caller `group_created`); the answer is the group's chat entry.
- `POST /requests/{id}/approve` on a direct-message request opens the direct chat. Its first message is the request's message, sent by the requester. The answer is the chat as the caller sees it; both users get `chat_created`.
- `POST /requests/{id}/reject` turns the request down; the requester is not told.
- Answering a request twice is `409`, and someone else's request is `404`. The request is locked while it is answered, so two answers cannot both win.
- The recipient gets `approval_request_created` for a new request.

## Groups

| Who | May |
|---|---|
| owner | everything below, delete the group, transfer ownership |
| admin (and v1 moderators) | change name, description and avatar; add and remove members (not the owner); make members admins or members again |
| member | read and write, leave |

In group messages, owners, admins and moderators may also delete anyone's message for everyone. The contract knows `owner`, `admin` and `member`; v1 moderators show as `admin` but keep their v1 rights (they cannot manage the group).

- **Role checks:** every group change checks the actor's role under the group's row lock in the transaction that makes the change (`ChatRepository.*As`), so a role taken away a moment before cannot still be used.
- **Members:** `POST /groups` (at most 200 members at once) and `POST /groups/{id}/participants` follow each invitee's `group_invites` setting, as in v1:
  - an explicit exception (allow, deny) decides first;
  - then `everyone`, `contacts` (a shared chat), `nobody` and the `*_except` values;
  - `wait_approval` makes an invitation that waits in the invitee's inbox; the group lists them once they accept;
  - a block denies.

  A member who does not allow invitations at all fails group creation (`403`) or the add (`403`). Adding a member twice is `409`.
- **Leaving and removing:** `POST /groups/{id}/leave` and `DELETE /groups/{id}/participants/{user_id}` remove the member and their pin of the group. The removed member gets `chat_deleted` and no further events of the group, and their unread counter for it is dropped. The owner cannot leave (`409`) or be removed (`403`) and has to transfer ownership first (`POST /groups/{id}/transfer-owner`; the old owner becomes an admin).
- **Avatar:** `PUT /groups/{id}/avatar` takes an image the caller uploaded with purpose `avatar`. Members download it through `/attachments/{id}/content`, which the group avatar's members may read.
- **Deleting:** `DELETE /groups/{id}` (the owner) deletes it for everyone. Members get `chat_deleted`, and the worker deletes the messages.
- **Events:** after every change the members get `group_updated` with the group as each of them sees it (members rendered with their privacy settings), and added members get `group_created`. These go out after the answer, because rendering for every member costs a directory lookup each.
