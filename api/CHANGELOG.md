# API contract changelog

Contract changes only. The backend changelog is `CHANGELOG.md` in the repository root. Rules: `docs/api-compatibility.md`.

## 2.1.0-alpha.21

- User sticker packs: `POST /attachments` takes `purpose: sticker` (PNG, JPEG or still WebP up to 5 MB, stored as a
  512 x 512 WebP of at most 512 KB). `POST /stickers/packs` creates a pack; `PATCH` and `DELETE
  /stickers/packs/{pack_id}` rename and delete it; `POST /stickers/packs/{pack_id}/stickers`, `PATCH` and `DELETE
  /stickers/packs/{pack_id}/stickers/{sticker_id}` and `PUT /stickers/packs/{pack_id}/order` manage its stickers.
  Only the owner may change a pack (`403`). Limits: 50 packs per user and 120 stickers per pack
  (`409 limit_reached`).
- `GET /stickers/packs?owned=true` lists the packs the user made. `GET /stickers/links/{slug}` opens a pack by its
  share link.
- `StickerPack` gains the required `is_mine` and `is_hidden` and the optional `owner_id` and `slug`.
- `POST /stickers/packs/{pack_id}/report` reports a pack; after 5 reports it is hidden for everybody but its owner
  and stickers cannot be sent from it.
- `ErrorCode` gains `limit_reached`.
- A sticker upload not yet in a pack can be loaded only by its uploader.

## 2.1.0-alpha.20

- Stickers: `MessageType` and `SendMessageRequest.type` gain `sticker`. A sticker message is sent with `sticker_id`
  only (no content, attachment or drawings) through `POST /chats/{chat_id}/messages`; the WebSocket `message` event
  does not take stickers. The server sets `content` to the sticker's emoji.
- `Message.sticker` returns the `Sticker` (id, pack, emoji, `is_animated`, `image`). `Message.attachment` carries
  the same image, so older clients show a sticker as a picture with its emoji as caption.
- New `stickers` operations: `GET /stickers/packs` (the user's panel), `GET /stickers/packs/{pack_id}` (any pack),
  `PUT` and `DELETE /stickers/packs/{pack_id}/installation` (add or remove a pack) and `GET /stickers/recent` (the
  last 20 stickers the user sent). Built-in default packs are in every panel until the user removes them.
- Sticker images are attachments any signed-in user can download.
- The search `type` filter accepts `sticker`.

## 2.1.0-alpha.19

- Typing status for voice messages: the WebSocket `typing` event gains `data.action` (`typing` or
  `recording_voice`). It is optional in the client event (default `typing`) and always set in the server event.
  Clients show an unknown value like `typing`.
- `PrivacySettings` has a required `typing_indicators_enabled` (default `true`) and `UpdatePrivacySettingsRequest`
  an optional one: with `false` the server drops the user's `typing` events, so nobody sees them typing.

## 2.1.0-alpha.18

- Drawings on images: `SendMessageRequest.drawings` adds strokes and text labels to images of a `file` message.
  The image file stays unchanged; the drawing is stored with the message.
- To draw on an image someone else sent, reply to that message with `reply_to`, reuse its image ID in
  `attachment_id` and send a drawing for it.
- `Attachment.drawing` returns the drawing. Clients show the image with it. Older clients ignore it and show the
  plain image.

## 2.1.0-alpha.17

- Media albums: `SendMessageRequest.attachment_ids` sends two to ten image or video uploads as one `file` message
  with one caption. `attachment_id` and `attachment_ids` are mutually exclusive; albums are `file` only.
- `Message.attachments` lists the album items in order. `Message.attachment` still carries the first item, so
  older clients show the first photo or video.
- `MediaKind` gains `visual` for `GET /chats/{chat_id}/media`: images and videos together.

## 2.1.0-alpha.16

- Trusted-device recovery no longer needs the current password. `DeviceRecoveryApproval.password` is no longer
  required, is deprecated and ignored. New optional `passkey` (`PasskeyVerification`).
- `POST /auth/recovery-requests/{requestId}/approve/options` (`DeviceRecoveryApprovalOptions`: `method` `passkey`
  with `challenge_id` and `public_key`, or `session` with `approvable_from` while the session is too new).
- New error codes `user_verification_required` (account has passkeys, no assertion sent) and `session_too_new`
  (no passkeys and the session is younger than the minimum session age), both `403` on the approval.

## 2.1.0-alpha.15

- End-to-end encrypted audio calls in direct chats and groups (tag `calls`, see `docs/calls.md` and
  `docs/calls-e2ee.md`). `GET /calls/config` (`CallConfig`: `enabled`, `max_participants`,
  `ring_timeout_seconds`) tells clients whether to show call controls; with `enabled: false` they hide them.
- `PUT /me/call-identity` (`CallIdentityRequest`, `204`) registers the session's P-256 call identity key
  (`CallIdentityKey`, 87 characters of base64url); a new key raises the epoch of a call the session is in.
- `GET /calls` (`CallList`) lists the user's calls that are not over; clients load it after every (re)connect.
  `GET /chats/{chat_id}/call` returns the chat's current call, `POST /chats/{chat_id}/call` (`StartCallRequest`
  with a required `client_request_id`, idempotent for 24 hours) starts one (`201`) or returns the running one
  (`200`). `GET /calls/{call_id}` reads a call, ended calls for 90 days.
- `POST /calls/{call_id}/join` (optional `JoinCallRequest` with `takeover`) answers or joins and returns
  `CallJoin` (`call` and a short-lived `CallGrant`: `url`, `token`, `identity`, `expires_at`);
  `POST /calls/{call_id}/grant` renews the grant for the session that joined. `POST /calls/{call_id}/decline`,
  `/leave` and `/end` answer `204`.
- `GET /calls/{call_id}/keys` (`CallKeyEnvelopeList`) and `POST /calls/{call_id}/keys` (`SendCallKeysRequest` with
  1 to 64 `OutgoingCallKeyEnvelope`) relay opaque HPKE envelopes (`CallKeyEnvelope`) for the current epoch.
- New schemas `Call` (`kind`, `state`, `end_reason`, `epoch`, `version`, `participants`, timestamps),
  `CallState` (`ringing`, `active`, `ended`), `CallEndReason` (`completed`, `missed`, `declined`, `cancelled`,
  `failed`), `CallParticipant` and `CallParticipantState` (`ringing`, `joined`, `declined`, `missed`, `left`).
- New error codes `calls_unavailable`, `calls_not_allowed`, `call_identity_required`, `call_full`, `call_ended`,
  `already_in_call` and `stale_epoch`. Every call operation except `GET /calls/config` can answer `503`
  (`CallsUnavailable`, with `Retry-After` when calls are only away for a moment). `403 calls_not_allowed` when the
  peer of a direct chat has calls off or one user blocked the other; `409` for `already_in_call`, `call_ended`,
  `call_full` and `stale_epoch`; `422 call_identity_required` when joining without a call identity.
- New WebSocket events `call_updated` (the whole `Call` to every chat member; keep the highest `version`) and
  `call_keys` (a `CallKeyEnvelopeList` for the recipient session only).
- `PrivacySettings` has a required `calls_enabled` (default `true`) and `UpdatePrivacySettingsRequest` an optional
  one: with `false` nobody can ring the user.
- `SystemEventKind` adds `call`: one `system` message per call, posted when it ends, with `actor` the initiator and
  `SystemEvent.call` a `CallSummary` (`call_id`, `kind`, `outcome`, `duration_seconds`).

## 2.1.0-alpha.14

- New schema `Location` (`city`, `country_code` (ISO 3166-1 alpha-2), `country` (English name), each nullable): the
  approximate location of a client address from an offline GeoIP database (IP geolocation by DB-IP). Clients
  localise the country name from `country_code`.
- `Session` (`GET /me/sessions`) and the items of `SecurityEventPage` (`GET /me/security-events`) have a required,
  nullable `location`, captured when the session signed in or refreshed and when the event happened.
- The `recovery_request` WebSocket event replaces `ip_hint` with a required, nullable `location`. No response or
  event exposes a client IP address or network.
- Operations that take an optional second-factor `code` answer `401` with `code: two_factor_required` when every
  other credential in the request was valid, the account has two-factor on and no `code` was sent; nothing
  single-use is consumed, so clients ask for the code and repeat the request. Invalid other credentials and a wrong
  `code` keep their errors. Affected: `POST /auth/recover/key`, `POST /auth/password-reset/confirm`,
  `POST /auth/recovery-requests/{requestId}/approve`, `POST /me/recovery-key`, `POST /me/2fa/disable`,
  `POST /auth/passkeys/register/options`, `PUT /me/email` and `DELETE /me/email`. `code` of
  `DisableTwoFactorRequest` is now optional.
- Sign in with a QR code. A device that is not signed in calls `POST /auth/qr-login` (public, `201`
  `QrLoginStart`: `request_id`, `poll_token`, `url` = `<RECOVERY_WEB_URL>/qr/<request_id>`, `expires_at` after 2
  minutes) and polls `POST /auth/qr-login/{requestId}/poll` with `{ poll_token }` (public, `QrLoginPollResult`:
  `status` `pending`, `scanned`, `denied` or `expired`). A signed-in device opens the link: `GET
  /auth/qr-login/{requestId}` (`QrLoginRequest` with `device`, `location`, `created_at`, `expires_at`) moves the
  request to `scanned` and binds it to that account, then `POST /auth/qr-login/{requestId}/approve` (optional
  `code`, `401 two_factor_required` with two-factor on and no code, `403 forbidden` for an idle or old session) or
  `POST /auth/qr-login/{requestId}/deny` answer `204`. The first poll after approval answers `status: approved`
  plus the `TokenResponse` fields and sets the refresh cookie, exactly once; later polls, a wrong `poll_token` and
  unknown requests answer `expired`. Unknown, expired or used requests answer `404` to the signed-in operations,
  already decided ones `409`.
- New security event types `qr_login_approved`, `qr_login_denied` (both with the new device's user agent and
  location) and `qr_login` (the new device signed in). The new `security_alert` WebSocket event
  carries informational notices (sign-ins, password, 2FA, passkey and recovery changes, `qr_login_approved`);
  `security_notice` stays limited to `password_reset`, so released clients that sign out on it keep working.
- `request_id` of the `recovery_request` WebSocket event is now an approval handle that differs from the
  requester's capability. `POST /auth/recovery-requests/{requestId}/approve` and `/deny` take this handle; status
  polling and `/complete` keep taking the requester's capability, so a signed-in device that receives the event can
  no longer poll or complete the request. `DeviceRecoveryApproval` has a new required `password`. Approve and deny
  answer `404` for an unknown, expired or already decided request and `403 forbidden` for an idle or old session.
- `POST /auth/passkeys/register/verify` answers `400 invalid_request` instead of `401 unauthenticated` for an
  expired or used registration challenge, so clients do not treat it as a lost session.
- A completed password recovery (key, device approval or email) removes every passkey and, except for an email
  recovery, the recovery email. The `password_reset` security event details gain `passkeys_removed` and
  `recovery_email_removed`.
- New `POST /auth/recovery-requests/{requestId}/cancel` (public, requester capability, `204` for every
  capability): withdraws a pending device recovery request so it stops counting towards the limit of three.
- `security_alert` adds `login_2fa` to its types and an optional `session_id`: the session that made the change,
  so clients can skip alerts about their own actions.
- `POST /auth/password-reset/confirm` answers `404 not_found` for an unknown, expired or used link instead of
  `400 invalid_credentials`, so clients can tell a dead link from a wrong code.

## 2.1.0-alpha.12

- `GET /link-preview?url=` returns the Open Graph preview of a public web page (`title`, `description`,
  `site_name`, `image`), cached for a day. `image.url` points to `GET /link-preview/image`, which streams the
  picture through the server (PNG, JPEG, GIF or WebP up to 5 MB) with a signature, so clients never load
  third-party images directly. Only public `http`/`https` addresses on ports 80 and 443 are fetched.
- Messages have an optional `system_event` (`kind`, `actor_id`, `actor_name`, `target_id`, `target_name`) on
  `system` messages, whose `content` is then `null`. Groups write them when a group is created and when members
  are added, join, are removed or leave. System messages do not count as unread.
- `GET /groups/{chat_id}/role-log` lists who changed whose role and when, newest first (members only).
- `GroupRole` and `UpdateRoleRequest` have `moderator` again: moderators may delete other members' messages.
- In a direct chat either side may delete any message for everyone (`DELETE /messages/{message_id}?scope=everyone`
  and `POST /chats/{chat_id}/messages/delete`).
- The last message of a chat (`GET /chats`, `GET /chats/{chat_id}`) skips messages deleted for everyone and is the
  newest message that still has content.

## 2.1.0-alpha.11

- Attachments have `display_url`: a JPEG of at most 1600 px (`GET /attachments/{attachment_id}/content?variant=display`)
  for showing a large still image in a viewer, much smaller than the original. It is `null` for small images and
  GIFs. Older images get their display copy on the first request.

## 2.1.0-alpha.10

- `GET /attachments/{attachment_id}/content` is cached for an hour again (`private, max-age=3600`) instead of a
  year, so a file is not kept in caches long after its message is deleted. Versioned avatar URLs keep the
  year-long cache.
- `POST /chats/{chat_id}/messages/delete` with `scope` everyone treats messages that are already deleted for
  everyone as done (`204` instead of `404`), so a retry after a partial failure deletes the rest.
- WebSocket: a connection with more than 64 client events waiting to be handled is closed with `1013`.
- WebSocket: a user has at most 10 connections per server; opening another closes the oldest with the new close
  code `4429`.
- `GET /users/{user_id}` and `GET /usernames/{username}` answer `404` for a user whose `search_visibility` hides
  them from the current user, unless the two share a chat, and are rate limited per user (`429`).
  `PUT /users/{user_id}/contact-name` follows the same rules and limit.
- `contacts` (and `shared_chats`) count a direct chat only once the user opened it or read a message in it, so
  opening an empty chat with someone does not make you their contact. Groups count as before.

## 2.1.0-alpha.9

- Privacy exceptions apply to every value of avatar, profile and presence visibility, not only to `*_except`:
  an `allow` exception always shows the item to that user and a `deny` exception always hides it. Group invites
  already worked this way.
- `direct_messages` and `search_visibility` take exception lists too (`PUT /me/privacy/exceptions/{setting_key}/{effect}`
  and `exceptions` of `PrivacySettings`): `allow` lets the user start a direct chat without approval or find you in
  search, `deny` never does.

## 2.1.0-alpha.8

- Adds `POST /chats/{chat_id}/messages/forward` (up to 20 messages to up to 20 chats, at most 20 copies, oldest
  first) and `POST /chats/{chat_id}/messages/delete` (up to 100 messages, `scope` me or everyone). Both check
  every message first and change nothing when one is refused.

## 2.1.0-alpha.7

- Adds `DELETE /me/avatars/{avatar_id}`: removes one of your avatars from the history and answers the updated
  `Me`. Deleting the current avatar makes the newest remaining one current again, or the default when none is left.

## 2.1.0-alpha.6

- `Message` adds optional `reply_preview` (`ReplyPreview`: id, type, sender, the first 100 characters of the
  text, attachment, is_deleted) on history pages and `message` events, so a reply shows what it answers even
  when that message is not loaded. It is absent when the answered message is gone or hidden for the recipient.

## 2.1.0-alpha.5

- A user's `avatar_url` adds `?rev={revision}`, which changes with every new avatar. `GET /users/{user_id}/avatar`
  accepts `rev`; while it matches the current avatar, the answer is `Cache-Control: private, max-age=31536000, immutable`.
- `GET /attachments/{attachment_id}/content` is cached for a year (`immutable`): an attachment's bytes never change.

## 2.1.0-alpha.4

- Editing a file message accepts an empty `content` (HTTP `PATCH /messages/{message_id}` and the WebSocket `edit`
  event): it removes the caption. A text message still needs at least one character.

## 2.1.0-alpha.3

- Adds `GET /notifications/avatar/{token}` and `POST /notifications/read/{token}` without a session, for the
  sender photo and the "Mark as read" action of a web push. Both answer `404` for a forged, expired or ended token.
- Web push payloads add `preview`, `read_token` and, with previews on, `sender_id` and `avatar_token`. Tokens are bound to the push
  device and its session.

## 2.1.0-alpha.2

- Adds notification preferences, session-bound push device registration and per-tab activity leases.
- Adds chat notification mutes for one hour, eight hours, indefinitely, or until unmuted.

## 2.1.0-alpha.1

- Registration returns a one-time recovery_key. GET /me adds recovery_setup_required.
- Adds GET/POST /me/recovery-key, POST /auth/recover/key and GET /me/security-events.
- Adds the security_notice WebSocket event for a recovery password reset.
- Recovery requires 2FA when enabled, rotates its key and sessions, and delays recovery-method changes for 24 hours. Legacy Shamir recovery remains unavailable.

## 2.0.0-alpha.8

Found by validating every response of the backend's HTTP tests against this contract:

- `GET /attachments/{attachment_id}/content` and `GET /users/{user_id}/avatar` list `206` (byte ranges), `304`
  (`ETag`) and `416`; the attachment download also its `302` to a signed URL.
- `POST /ws/ticket` answers `201`, as listed (the server sent `200`).
- The conventions name the statuses any operation may answer: `413`, `415`, `500`, `503`.

## 2.0.0-alpha.7

Search:

- New `GET /search/messages`: full-text search across all chats of the caller, with filters (`chat_id`, `sender_id`,
  `type`, `from`, `to`) and `search_after` paging.
- `SearchHit.highlight` wraps the matched words in U+E000 and U+E001.
- `GET /chats/{chat_id}/messages/search` can answer `429` (searches are rate limited).

## 2.0.0-alpha.6

Groups, found while implementing them:

- `POST /groups` can answer `403`: a member who does not allow invitations from the caller fails the request.
- `PUT /groups/{chat_id}/avatar` (`422`: not an image the caller uploaded as an avatar) and
  `POST /groups/{chat_id}/transfer-owner` (`422`: to yourself) document their validation errors.

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
