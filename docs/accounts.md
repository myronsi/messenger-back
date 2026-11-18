# Accounts and users

The profile, privacy, blocking, contact-name and user lookup endpoints (`internal/httpapi/account_server.go`). Users are always rendered for the viewer by `internal/users.Directory`, which applies the privacy settings, contact names and blocks: a user who blocked the viewer shows no avatar, bio or presence to them, whatever their settings say.

## Profile

- `PATCH /me` changes only the fields it sends; changing `display_name` or `username` needs an account status below `restricted` ([moderation.md](moderation.md)); `"bio": null` clears the bio. The display name is trimmed and must not be empty; unknown fields are rejected (`422`). Only the sent columns are written, so two devices editing different fields at once do not undo each other.
- `DELETE /me` needs the current password (`403` if wrong; repeated failures are rate limited like logins, `429`). In one transaction it deletes the user's sessions, direct chats and the groups nobody else is in, hands the ownership of other groups to the next member (admins first, then moderators, then the longest-standing member) and leaves them. Afterwards the API:
  - announces the deleted chats to their other members (`removed` frames) and drops their unread counters;
  - emits `chat.deleted` (the worker deletes the messages) and `chat.member_removed` for the groups left;
  - invalidates the membership cache and drops the user's own unread counters;
  - deletes the files and thumbnails of the deleted chats' v1 attachments (v2 uploads become unreferenced and are collected after a day);
  - emits `user.deleted`, drops the deleted sessions from the session cache and closes their sockets.

  Access tokens stop working at once; only when Redis cannot be reached does a cached session survive, at most for the session cache TTL. The remaining members of a group the user left learn about the change (member list, new owner) when they reload the group; live group events come with the group endpoints.

## Privacy

`GET`/`PATCH /me/privacy`. `PATCH` changes only the settings it sends.

| Setting | Values |
|---|---|
| `avatar_visibility`, `profile_visibility`, `presence_visibility`, `last_seen_visibility`, `birthday_visibility` | `everyone`, `contacts`, `nobody` |
| `group_invites` | the same, and `wait_approval` (default `contacts`) |
| `direct_messages` | `everyone`, `contacts`, `wait_approval`, `nobody` |
| `calls_from` | `everyone`, `contacts`, `nobody` ([calls.md](calls.md)) |
| `search_visibility` | `everyone`, `nobody` |
| `read_receipts_enabled` | `true`, `false` |
| `calls_enabled` | deprecated: `false` exactly when `calls_from` is `nobody`; writing `false` sets `nobody`, writing `true` sets `everyone` when it was `nobody` |
| `typing_indicators_enabled` | `true`, `false`: with `false` the server drops the user's `typing` events ([realtime.md](realtime.md#client-events)) |

The contract's `contacts` means "people you share a chat with" (`shared_chats` in the schema): a direct chat that you opened yourself or read a message in, or a group that you took part in. You take part in a group by creating it, by joining it through an invitation you accepted, or by reading or writing in it (`participants.engaged`, migration 000041; groups you were in before that migration count). A direct chat someone else opened with you and that you never read in, or a group someone added you to and that you never read or wrote in, does not make them your contact. So nobody can see what you show only to contacts, find you through a lookup, message you under `direct_messages: contacts` or ring you under `calls_from: contacts` just by adding you to a group.

The deprecated values `everyone_except` and `nobody_except` (and `shared_chats` for `direct_messages`) are still accepted and stored as `everyone`, `nobody` (and `contacts`); responses never contain them. Migration 000041 rewrote the stored ones. `PUT /me/privacy/exceptions/{setting}/{allow|deny}` replaces one exception list; the lists apply to every value of the setting. A user can be on only one list per setting, so putting them on one removes them from the other. Unknown user IDs are rejected.

## Blocking and contact names

- `PUT`/`DELETE /me/blocked-users/{id}`; `GET /me/blocked-users` pages by block time (newest first; the cursor is opaque). Both are idempotent: unblocking a user who is not blocked is `204` too; only an unknown user is `404`.
- A blocked user does not find the blocker in search, sees none of the blocker's guarded profile fields, and cannot send, edit, react or type in their direct chat.
- `PUT`/`DELETE /users/{id}/contact-name` sets the name the viewer sees for that user.

## Reports and account status

- `POST`/`DELETE /users/{id}/report` reports a user (reason, optional note, block too by default) or withdraws the report. The reported user never learns who reported them.
- Reports drive the reported account's status automatically: `good`, `warned`, `limited`, `restricted`, `suspended`. `GET /me/account-status` shows the status, what it restricts and whether an appeal is possible; `POST /me/account-status/appeal` starts a 14-day probation one level lower. The WebSocket event `account_status` reports changes to the user's own sessions.
- A restricted action answers `403` `account_restricted` with `restriction`. `restricted` and `suspended` users are left out of `GET /users?q=`; a suspended user carries `account_label: restricted` wherever other users see them.
- Weights, thresholds, decay, appeals and enforcement: [moderation.md](moderation.md).

## Lookup and search

- `GET /users/{id}` and `GET /usernames/{username}` answer `404` for users that do not exist, and `400` for an id that is not a number.
- `GET /users?q=` finds users whose username starts with the query or whose display name contains it (case-insensitive; `%`, `_` and `\` are matched literally), ordered by username, paged by the last username. The query needs at least 2 characters. Users with `search_visibility: nobody`, users who blocked the viewer and users whose account status is `restricted` or `suspended` are left out. Searches are rate limited per user.
- `GET /users/{id}` and `GET /usernames/{username}` answer `404` for a user whose `search_visibility` (with its exceptions) hides them from the viewer, unless the two share a chat; the viewer's own profile is always found. Lookups are rate limited per user (120 per minute, bursts of 60). `PUT /users/{id}/contact-name` follows the same rules and shares the limit.
- The username prefix uses the `users_username_prefix_idx` index. The display-name match uses the trigram index `users_display_name_trgm_idx` (`pg_trgm`, migration 000008), which needs 3 characters, so a 2-character query matches usernames only.
- Full-text search across messages is Elasticsearch (B40).

## Versions

`GET /meta` (no authentication, cacheable for 60 s) reports:
- `backend_version`, set at build time with `-ldflags -X …/internal/version.Backend=…` (the image takes `--build-arg VERSION=`);
- `commit` (`APP_COMMIT`);
- `api_version` and `min_client_api_version`.

Requests with an `X-Client-Api-Version` below `MIN_CLIENT_API_VERSION` or with another major version get `426 client_outdated`; a malformed value gets `400 invalid_client_version`. `/meta`, `/ws` (which reports versions in `hello`) and the operational endpoints are never blocked.

Requests are counted per contract version in `messenger_client_api_requests_total`. The label drops build metadata, and pre-releases other than `alpha.N`, `beta.N` and `rc.N` are counted as `other`, so made-up versions cannot use up the label slots.
