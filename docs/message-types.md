# Polls, contacts, locations and group system lines

Contract 2.1.0-alpha.47. Migrations: PostgreSQL `000058_message_types`, ScyllaDB `000007_message_payload`.

## Message types

| Type | Sent with | Stored | `content` (for older clients) |
|---|---|---|---|
| `poll` | `poll` (`PollCreate`) | PostgreSQL `polls` and `poll_votes` | `📊 <question>` |
| `contact` | `contact.user_id` | ScyllaDB `messages.payload` `{"user_id":"…"}` | `👤 @<username>` |
| `location` | `location` (`LocationCreate`) | ScyllaDB `messages.payload` `{"lat","lon","accuracy_m","title","sig"}` | `📍 <title> <OpenStreetMap link>` |

- The three types are sent over REST only (`POST /chats/{chat_id}/messages`), like stickers. They have no content,
  attachment, sticker or drawings in the request; the server writes the summary into `content`. Older clients show
  every unknown type as a text bubble with `content`, and the moderation console shows it as text.
- They cannot be edited. Reply, forward, pin and delete work as for any message.
- Mentions and link previews are never made from them. Search indexes the poll question and options and the place
  name, never contacts. Pushes carry `content_type` `poll`, `contact` or `location` (see
  [notifications.md](notifications.md)).
- Deleting a message for everyone, clearing the history for everyone and removal by moderation delete the poll and
  its votes; `payload` is cleared with the content.

## Polls

- Any member who may send may create a poll: 2 to 10 options of at most 100 characters that differ ignoring case,
  a question of at most 300. `multiple_answers`, `anonymous` (default `true`), `quiz` (`correct_option`,
  `explanation` up to 200; not with `multiple_answers`) and `close_at` (5 minutes to 30 days ahead).
- `Poll` is rendered per viewer (`messages.PollViewFor`): counts, `total_voters`, `is_correct` and `explanation`
  only once the viewer voted, created the poll, or the poll is closed. `can_close` for the creator and, in groups,
  the owner and admins.
- `PUT …/poll/votes` replaces the caller's votes in one transaction that locks the poll row
  (`internal/store/postgres/polls.go`): `409 poll_closed` after closing, `409 quiz_answered` for a second quiz
  answer, `422` for indexes out of range or two answers in a single-answer poll. The same vote again changes
  nothing. A change raises `version`, sends `poll_updated` (rendered per recipient, `realtime.Fanout.PollChanged`)
  and records a `poll` sync change, which `GET /sync/changes` turns into `message_updated`.
- `GET …/poll/voters` lists voters of one option, newest vote first, only for public polls whose results the caller
  sees. Anonymous votes are stored with the voter (one vote per member, changing it), but no one else sees them.
- Every API instance runs `RunPollCloser` every 30 s: it closes due polls with `FOR UPDATE SKIP LOCKED` (so
  instances never close one twice) and sends `poll_updated`. A poll whose `close_at` has passed is shown as closed
  even before that.
- A forwarded poll is a new poll with the same question, options and settings, without votes or closing time.
- Votes of a member who left stay, like reactions.

## Contacts

The sender may share any user whose profile they can open (`users.Directory.ForViewer`: not deleted, findable or
sharing a chat), including themselves; otherwise `403`. Each recipient sees the shared user as their own privacy
view allows at the time they load the message, not as they were when it was sent. `contact.user` is `null` when the
account is gone. Chat-list previews leave `contact` and `poll` out.

## Locations

Coordinates are rounded to 6 decimals. The map image URL is signed with `maps.Signer` at send time; see
[maps.md](maps.md).

## Group system lines

| Kind | Posted by | Fields |
|---|---|---|
| `group_renamed` | `PATCH /groups/{chat_id}` with a different name | `old_name`, `new_name` |
| `group_avatar_changed` | `PUT /groups/{chat_id}/avatar`; `DELETE /groups/{chat_id}/avatar` when there was a photo | `avatar_removed` |
| `owner_transferred` | `POST /groups/{chat_id}/transfer-owner` | `actor` the previous owner, `target` the new one |

Like all system lines they send no push and do not count as unread.
