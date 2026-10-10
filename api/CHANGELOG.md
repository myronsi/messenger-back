# API contract changelog

Contract changes only. The backend changelog is `CHANGELOG.md` in the repository root. Rules: `docs/api-compatibility.md`.

## 2.0.0-alpha.5

Chats, found while implementing the chat list:

- `DELETE /chats/{chat_id}` deletes a direct chat for both users (as v1 did), not "for yourself"; a group is `422`.
- `POST /chats` can answer `422` (a chat with yourself, a too long `initial_message`).
- `PUT /chats/{chat_id}/pin`: at most 10 pinned chats (`409`).
- `GET /chats` documents its order, what `last_message` and its `read_by` hold, and that `unread_count` stops at 999.
- `DELETE /me` can answer `429` (repeated wrong passwords).

## 2.0.0-alpha.4

Media, found while implementing uploads:

- Attachment IDs are UUIDs (`AttachmentRef`, `format: uuid`) instead of decimal IDs: `Attachment.id`, the `attachment_id` path parameter, `attachment_id` of `SendMessageRequest`, `SetAvatarRequest` and the WebSocket `message` event.
- `GET /attachments/{attachment_id}/content` takes `variant=thumbnail`, may answer `302` to a signed URL, and documents its `Content-Disposition` rules.
- `GET /users/{user_id}/avatar` takes `version` (an `AvatarVersion.id`), which `AvatarVersion.url` points to.

## 2.0.0-alpha.3

WebSocket details found while implementing the gateway (documentation of `websocket.md` only):

- Optional `api_version` query parameter on `/api/v2/ws`: browsers cannot send `X-Client-Api-Version` on a WebSocket, so a client passes its contract version here and an outdated one gets `hello` and then close code `4426`.
- Close codes: `4401` also when the session of the connection ends (logout, revoked session, password change); `1012` "reconnect" also after the server lost its pub/sub connection (events may be missing, so the client catches up); `1013` when the client cannot keep up with its events.
- A user removed from a chat gets `chat_deleted` and no further events of that chat.

## 2.0.0-alpha.2

Authentication details found while implementing the Go backend:

- Session IDs (`Session.id`, `session_id` path parameter) are UUIDs (`format: uuid`) instead of decimal IDs.
- `code` of `POST /auth/login/2fa` and `POST /me/2fa/disable` accepts an authenticator code or a recovery code.
- `POST /me/2fa/confirm` answers `200` with `recovery_codes` (shown once) instead of `204`.
- `POST /me/password` can answer `403` (wrong current password); `POST /me/2fa/confirm` and `/me/2fa/disable` can answer `409`.
- The refresh cookie is scoped to `/api/v2/auth/refresh`.

## 2.0.0-alpha.1

First draft of API contract v2 for the Go backend (`/api/v2`), published as `@myronsi/messenger-api@2.0.0-alpha.1`:

- OpenAPI 3.1 document for all REST endpoints of the parity list.
- WebSocket protocol: one connection per user with a ticket, an event envelope, `client_temp_id` acknowledgements and JSON Schemas for every event.
- Resource-style routes, string IDs, cursor pagination, `application/problem+json` errors with stable `code`s, typed messages with `attachment_id`.
