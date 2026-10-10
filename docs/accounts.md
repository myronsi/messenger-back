# Accounts and users

The profile, privacy, blocking, contact-name and user lookup endpoints (`internal/httpapi/account_server.go`). Users are always rendered for the viewer by `internal/users.Directory`, which applies the privacy settings, contact names and blocks.

## Profile

- `PATCH /me` changes only the fields it sends; `"bio": null` clears the bio. The display name is trimmed and must not be empty; unknown fields are rejected (`422`).
- `DELETE /me` needs the current password (`403` if wrong; repeated failures are rate limited like logins). In one transaction it deletes the user's direct chats and the groups nobody else is in, hands the ownership of other groups to the next member (admins first, then moderators, then the longest-standing member) and leaves them. Afterwards the API announces the deleted chats to their other members (`removed` frames), drops their unread counters, emits `chat.deleted` (the worker deletes the messages) and `chat.member_removed` for the groups left, invalidates the membership cache, deletes the files of the deleted chats and emits `user.deleted`. The access tokens of the account stop working immediately because its sessions are gone.

## Privacy

`GET`/`PATCH /me/privacy`. The visibility settings (`avatar_visibility`, `profile_visibility`, `last_seen_visibility`, `search_visibility`, …) accept `everyone`, `contacts` and `nobody`; the contract's `contacts` means "people you share a chat with" (`shared_chats` in the schema). `PUT /me/privacy/exceptions/{setting}/{allow|deny}` replaces one exception list; a user can be on only one list per setting, so putting them on one removes them from the other. Unknown user IDs are rejected.

## Blocking and contact names

- `PUT`/`DELETE /me/blocked-users/{id}`; `GET /me/blocked-users` pages by block time (newest first; the cursor is opaque). Unblocking a user who is not blocked is `404`.
- A blocked user does not find the blocker in search and cannot send, edit, react or type in their direct chat.
- `PUT`/`DELETE /users/{id}/contact-name` sets the name the viewer sees for that user.

## Lookup and search

- `GET /users/{id}` and `GET /usernames/{username}` answer `404` for users that do not exist.
- `GET /users?q=` finds users whose username starts with the query or whose display name contains it (case-insensitive; `%`, `_` and `\` are matched literally), ordered by username, paged by the last username. The query needs at least 2 characters. Users with `search_visibility: nobody` and users who blocked the viewer are left out. Searches are rate limited per user. A username prefix uses the `users_username_prefix_idx` index and the display-name match uses the trigram index `users_display_name_trgm_idx` (`pg_trgm`, migration 000008). Full-text search across messages is Elasticsearch (B40).

## Versions

`GET /meta` (no authentication, cacheable for 60 s) reports `backend_version` (set at build time with `-ldflags -X …/internal/version.Backend=…`; the image takes `--build-arg VERSION=`), `commit` (`APP_COMMIT`), `api_version` and `min_client_api_version`. Requests with an `X-Client-Api-Version` below `MIN_CLIENT_API_VERSION` or with another major version get `426 client_outdated`; a malformed value gets `400 invalid_client_version`. `/meta`, `/ws` (which reports versions in `hello`) and the operational endpoints are never blocked. Requests are counted per contract version in `messenger_client_api_requests_total`.
