# API contract changelog

Contract changes only. The backend changelog is `CHANGELOG.md` in the repository root. Rules: `docs/api-compatibility.md`.

## 2.1.0-alpha.51

- `UploadPurpose` `sticker` accepts animated WebP as a source of an animated sticker, like an animated GIF: the server
  keeps at most 3 s, crops it when asked and stores a looping animated WebP of at most 512 x 512 px. Before, such
  files got `415`.

## 2.1.0-alpha.50

- Synced settings: `GET` and `PATCH /me/client-settings` (`ClientSettings`, `UpdateClientSettingsRequest`,
  `AppearanceSettings`): appearance, recent emoji (24) and recent reactions (8) shared by all devices.
- Chat backgrounds: `GET /me/wallpapers` (`WallpaperList`), `PUT` and `DELETE /me/wallpapers/default`, `PUT` and
  `DELETE /chats/{chat_id}/wallpaper` (`SetChatWallpaperRequest` with `for_both` for direct chats), `Wallpaper`,
  `WallpaperEntry`, `WallpaperPattern`. `UploadPurpose` gains `chat_wallpaper`; `SystemEventKind` gains
  `chat_wallpaper_set`.
- Playlists: `GET` and `POST /me/playlists`, `GET`, `PATCH` and `DELETE /me/playlists/{playlist_id}`,
  `PUT /me/playlists/{playlist_id}/tracks` (`Playlist`, `PlaylistSummary`, `PlaylistTrack` with `available`). At most
  100 playlists and 1000 tracks each.
- WebSocket server events `client_settings_updated`, `wallpaper_updated` and `playlists_updated`, only to the user's
  own sessions.

## 2.1.0-alpha.49

- Chat imports hold up to 500,000 messages: `ChatImport.message_count` and `StartChatImportRequest.message_count`
  allow up to `500000`.
- `UploadPurpose` `message` accepts animated WebP (for example WhatsApp stickers in an import) and keeps the
  animation; before, such files got `415`.
  
## 2.1.0-alpha.48

- Video and screen sharing in calls: `CallConfig` (`GET /calls/config`) gains `video_enabled` and
  `screen_share_enabled`. When they are on, the media grant also allows the `camera`, `screen_share` and
  `screen_share_audio` sources; every source is end-to-end encrypted with the same per-epoch sender keys as audio.

## 2.1.0-alpha.47

- New message types `poll`, `contact` and `location` (`MessageType`, `SendMessageRequest.type`, `UnsentMessage.type`),
  sent over REST only. `SendMessageRequest` and `UnsentMessage` gain `poll` (`PollCreate`), `contact`
  (`ContactCreate`) and `location` (`LocationCreate`); `Message` gains `poll` (`Poll`), `contact` (`ContactCard`) and
  `location` (`MessageLocation`). Their `content` holds a plain-text summary for older clients.
- Polls: `PUT /chats/{chat_id}/messages/{message_id}/poll/votes` (`votePoll`), `POST …/poll/close` (`closePoll`) and
  `GET …/poll/voters` (`listPollVoters`). New schemas `PollQuizCreate`, `PollOption`, `PollVoteRequest`; `ErrorCode`
  gains `poll_closed` and `quiz_answered`. New WebSocket server event `poll_updated`.
- Maps: `GET /maps/tiles/{z}/{x}/{y}` (`getMapTile`) and `GET /maps/static` (`getStaticMap`), new tag `maps`, new
  schema `MapImage`.
- `SystemEventKind` gains `group_renamed`, `group_avatar_changed` and `owner_transferred`; `SystemEvent` gains
  `old_name`, `new_name` and `avatar_removed`. New `DELETE /groups/{chat_id}/avatar` (`deleteGroupAvatar`).
- Stickers from videos and GIFs: `UploadAttachmentRequest` gains `crop_x`, `crop_y` and `crop_size`; `start_ms` and
  `end_ms` also apply to stickers.

## 2.1.0-alpha.46

- Chat import from Telegram and WhatsApp: `GET`/`POST /chats/{chat_id}/imports` (`listChatImports`,
  `startChatImport`), `GET`/`DELETE /imports/{import_id}` (`getChatImport`, `removeChatImport`),
  `PUT /imports/{import_id}/messages` (`addImportedMessages`), `POST /imports/{import_id}/finish`
  (`finishChatImport`), `PUT /imports/{import_id}/senders/{sender_key}` (`assignImportSender`) and
  `POST /imports/{import_id}/senders/{sender_key}/decision` (`decideImportSender`). New schemas `ChatImport`,
  `ChatImportList`, `ChatImportSender`, `ChatImportState`, `ImportSource`, `ImportSenderKey`, `ImportSenderState`,
  `StartChatImportRequest`, `StartChatImportSender`, `ImportedMessage`, `ImportedMessageType`,
  `AddImportedMessagesRequest`, `ImportProgress`, `AssignImportSenderRequest`, `DecideImportSenderRequest`,
  `MessageImport` and `ImportSummary`; new path parameters `ImportId` and `ImportSenderKey`.
- `Message` gains `import`; for imported messages `created_at` is the time in the other messenger, and `sender` is
  `null` until the member behind the name confirms it.
- `SystemEventKind` gains `history_imported`, `history_import_ended` and `imported_note`; `SystemEvent` gains
  `import` and `text`.
- `SyncChange.type` gains `history_imported`.
- WebSocket server events `import_update` and `import_sender_update`.

## 2.1.0-alpha.45

- `GET /search/messages` gains the query parameter `kind` (`MediaKind`: `image`, `visual`, `audio`, `voice`, `file`,
  `link`) to find only messages with that kind of attachment or with a web link. `q` is now optional when `kind` is
  given, which lists those messages newest first.

## 2.1.0-alpha.44

- `GET /sync/changes` (`listSyncChanges`): edits, reaction changes, deletions, pin changes, cleared histories and
  chats deleted for the caller since a cursor, for clients that keep messages offline. New schemas
  `SyncChangesPage` and `SyncChange`.
- `POST /me/storage-key` (`getStorageKey`): the key of the current session for encrypting offline data, new schema
  `StorageKey`.

## 2.1.0-alpha.43

- Device-bound sessions: optional header parameter `DeviceProof` (`Device-Proof`, a DPoP-style ES256 proof) on
  `POST /auth/register`, `/auth/login`, `/auth/login/2fa`, `/auth/refresh`, `/auth/recover/key`,
  `/auth/recovery-requests/{requestId}/complete`, `/auth/qr-login/{requestId}/poll` and
  `/auth/passkeys/login/verify`. A refresh of a bound session without a proof of its key answers `401` and ends the
  session.
- `ErrorCode` gains `device_proof_stale`.
- `Date` is exposed to cross-origin clients.

## 2.1.0-alpha.42

- Pinned messages: `PUT /chats/{chat_id}/messages/{message_id}/pin` (`PinMessageRequest` with `PinScope` `me` or
  `everyone`, returns `PinnedMessage`), `DELETE /chats/{chat_id}/messages/{message_id}/pin?scope=` and
  `GET /chats/{chat_id}/pins` (`PinnedMessagePage`). At most 500 pins for everyone per chat and 500 own pins per member
  and chat (`409` `limit_reached`).
- `Chat.pinned_message_count`.
- `SystemEventKind` gains `message_pinned` and `message_unpinned`; `SystemEvent` gains `message_id`, `snippet` and
  `message_type`.
- WebSocket server event `pin_update`.

## 2.1.0-alpha.41

- Public cards without an account (tag `public`, `security: []`, rate limited per IP, `X-Robots-Tag: noindex`):
  `GET /public/names/{public_name}` (`PublicNameLookup`: `PublicNameKind` `user`, `group`, `unavailable`, with
  `PublicUser` or `PublicGroup`), `GET /public/invites/{token}` (`PublicGroup`), their avatars
  `GET /public/names/{public_name}/avatar` and `GET /public/invites/{token}/avatar`, and link preview pages for
  crawlers `GET /og/names/{public_name}` and `GET /og/invites/{token}` (`text/html`).
- `PrivacySettings.web_profile_visibility` (`WebProfileVisibility`: `everyone`, `nobody`; default `everyone`);
  `UpdatePrivacySettingsRequest` accepts it.
- Group invite links: `GET/POST /groups/{chat_id}/invite-links`, `PATCH/DELETE /groups/{chat_id}/invite-links/{link_id}`
  (`InviteLink`, `InviteLinkList`, `CreateInviteLinkRequest`, `UpdateInviteLinkRequest`), `GET /invites/{token}`
  (`GroupPreview`) and `POST /invites/{token}/join` (`Chat`).
- Public groups: `PUT/DELETE /groups/{chat_id}/public-name` (`SetPublicNameRequest`), `Group.public_name`
  (`PublicName`, one name space with usernames), `GET /public-groups` (`GroupPreviewPage`),
  `GET /public-groups/{public_name}`, `POST /public-groups/{public_name}/join` and
  `GET /public-names/{public_name}/availability` (`PublicNameAvailability`).
- Bans: removing a member bans them; `GET /groups/{chat_id}/bans` (`GroupBanPage` of `GroupBan`) and
  `DELETE /groups/{chat_id}/bans/{user_id}`.
- `AccountRestriction` gains `share_group` and `join_by_link`.
- Mentions: `Message.mentions` (member IDs) and `Message.mentions_all` (`@all` from an owner or admin);
  `Chat.unread_mentions_count`; `GET /chats/{chat_id}/mentions` (`MentionList`);
  `NotificationPreferences.mentions_in_muted` (default `true`).

## 2.1.0-alpha.40

- `RegisterRequest` gains the optional `terms_version` (the date of the accepted Terms of Use, such as `2026-11-19`)
  and `age_confirmed` (at least 13, a parent or guardian agrees under 16). `age_confirmed: false` or a
  `terms_version` that is not such a date is `422` with the field. When the server requires terms acceptance, a
  registration without `terms_version` or without `age_confirmed: true` is `422` as well.
- `Me` gains the required, nullable `terms_version` and `terms_accepted_at`.

## 2.1.0-alpha.39

- Content reports: `POST /chats/{chat_id}/messages/{message_id}/report` and `POST /groups/{chat_id}/report`
  (`ContentReportRequest`: `ContentReportReason`, which adds `illegal` with a required `note`), and `DELETE` on both
  to withdraw. User, message and group reports share the limit of 20 a day.
- `Message.removed_by_moderation` and, for the sender only, `Message.moderation_reason` (`ModerationRemovalReason`:
  a `ModerationReason` `code` and a `text`); `ReplyPreview.removed_by_moderation`. The WebSocket `delete` event gains
  the optional `removed_by_moderation` and, in the sender's copy, `moderation_reason`.

## 2.1.0-alpha.38

- Review by a person: `POST /me/account-status/review` (`AccountReviewRequest`: `text`, 1 to 500 characters) stores a
  request for the moderation team while the status is not `good`; one open request at a time (`409`), at most 3 in
  30 days (`429`). `AccountStatus` gains `review_requested_at` and `can_request_review`, also in the
  WebSocket event `account_status`.

## 2.1.0-alpha.37

- `AccountStatus` gains the optional `decided_by` (`AccountStatusDecidedBy`: `automatic`, `staff`), `reason_code`
  (`ModerationReason`: `spam`, `scam`, `harassment`, `impersonation`, `inappropriate_profile`, `illegal`, `other`) and
  `reason_text` for statuses a person on the moderation team set. `can_appeal` is `false` for those.
- Reporter outcome notices: `GET /me/report-outcomes` (`ReportOutcomeList` of `ReportOutcome`: `ReportSubjectType`,
  `ReportOutcomeKind`) and `POST /me/report-outcomes/{outcome_id}/read`; the WebSocket event `report_outcome` carries a
  new one to the reporter's own sessions.

## 2.1.0-alpha.36

- User reports: `POST /users/{user_id}/report` (`ReportUserRequest`: `ReportReason`, optional `note`, `block`
  default `true`) and `DELETE /users/{user_id}/report`.
- Automatic account status: `GET /me/account-status` and `POST /me/account-status/appeal` return `AccountStatus`
  (`AccountStatusLevel`, `AccountRestriction`, including `manage_group` for group changes of a suspended
  account); the WebSocket event `account_status` carries it to the user's own sessions.
- `ErrorCode` gains `account_restricted` (`403`); `Problem.restriction` and the WebSocket `error` event's
  `data.restriction` name the blocked action.
- `User.account_label` (`AccountLabel`: `restricted`) marks suspended accounts.

## 2.1.0-alpha.35

- `PrivacySettings` gains the required `voice_video_messages` (`VoiceVideoPolicy`: `everyone`, `contacts`) and
  `forward_link` (`ForwardLinkPolicy`: `everyone`, `contacts`, `nobody`); `UpdatePrivacySettingsRequest` accepts both.
  `PUT /me/privacy/exceptions/{setting_key}/{effect}` accepts both keys, and `PrivacySettings.exceptions` lists them.
- A voice message, or a file message with a video, sent, resent or forwarded into a direct chat whose peer does not
  accept it is refused with `403` `forbidden` and the field `voice_video_messages`. The WebSocket `error` event gains
  the optional `field`.
- `User.can_send_voice_video`, only on `Chat.peer` of a direct chat. When the peer changes `voice_video_messages` or its
  exceptions, a `chat_list_update` brings the new value.
- `ForwardedFrom.sender_id` is `null` for viewers that the original sender's `forward_link` excludes.

## 2.1.0-alpha.34

- Requests folder: `Chat` gains the required `is_request`, `true` when a stranger opened the direct chat and the caller
  has not replied, accepted, pinned or archived it yet. Messages in such a chat send the caller no push.
- `GET /chats` gains `folder` (`main`, the default, `requests` or `all`). Without it the answer holds only the main list.
- `POST /chats/{chat_id}/accept` moves a chat out of the Requests folder; `GET /chats/requests/count` answers
  `ChatRequestCount` (`requests_count`).
- WebSocket: `chat_list_update` also goes out when a chat moves out of the Requests folder.

## 2.1.0-alpha.32

- `Attachment` gains `placeholder` (ThumbHash), `processing` (`MediaProcessing`: `queued`, `running`, `done`,
  `failed`), `source_url`, `animation_url`, `panel_url` and `message_url`. `thumbnail_url` is a 480 px WebP for new
  uploads and the cover of an audio file; `tags` (with the new `album`) is set for audio files, not only profile music.
- `GET /attachments/{attachment_id}/content` gains the variants `source`, `panel`, `message`, `animation`, `inline`,
  `avatar_64`, `avatar_128` and `avatar_256`, and `pt`: it works without a bearer token when `pt` is a playback token.
  Without `variant`, an optimized video or audio file answers with the optimized copy.
- `POST /attachments/{attachment_id}/playback` returns `PlaybackUrl`, a five-minute URL for media elements.
- Resumable uploads: `POST /upload-sessions`, `GET`/`DELETE /upload-sessions/{upload_id}`,
  `PUT /upload-sessions/{upload_id}/chunks` and `POST /upload-sessions/{upload_id}/complete`.
- `GET /users/{user_id}/avatar` gains `size`; `GET /emoji/items/{emoji_id}/image` gains `variant=inline`.
- WebSocket: `attachment_updated` carries an attachment whose optimized copy is ready or failed.

## 2.1.0-alpha.31

- Custom emoji: `CustomEmoji`, `EmojiPack` and the `emoji` tag with `GET`/`POST /emoji/packs`, `GET`/`PATCH`/`DELETE
  /emoji/packs/{pack_id}`, `PUT`/`DELETE /emoji/packs/{pack_id}/installation`, `POST /emoji/packs/{pack_id}/emoji`,
  `PATCH`/`DELETE /emoji/packs/{pack_id}/emoji/{emoji_id}`, `POST /emoji/packs/{pack_id}/report`, `GET /emoji/links/{slug}`,
  `GET /emoji/items/{emoji_id}`, `GET /emoji/items/{emoji_id}/image` and `GET /groups/{chat_id}/emoji` (one set per group, edited by the
  owner and admins).
- Message text, captions, display names and bios may hold the tokens `<:shortcode:id>` and `<a:shortcode:id>`; the
  server turns tokens the sender may not use into `:shortcode:`. `Reaction.emoji` may be `c:<id>`.
- `UploadPurpose` gains `emoji`. `UpdateMeRequest.display_name` allows up to 400 characters and `bio` up to 4000 (the
  visible text keeps its limits of 64 and 500, counting each token as its `:shortcode:`).
## 2.1.0-alpha.30

- Connected accounts (`Connection`, `ConnectionList`, `ConnectionProvider`): `GET /me/connections`,
  `PUT /me/connections/order`, `DELETE /me/connections/{connection_id}` and `GET /users/{user_id}/connections`
  (visible like the bio). At most 15 per user.
- Providers that sign the user in: `POST /me/connections/{provider}/start` returns a `redirect_url`; the provider
  sends the browser back to `GET /connections/callback/{provider}`, which redirects to the web app with `provider` and
  `result`.
- Bluesky, Mastodon and websites: `POST /me/connections/{provider}/claim` returns a code to publish,
  `POST /me/connections/{provider}/verify` checks it.
- `Meta` gains the required `connection_providers`: the providers this server offers.

## 2.1.0-alpha.29

- `User` gains the required `pronouns`, `birthday` (`Birthday`: day, month, optional year), `music`
  (`ProfileMusic`) and `background` (`ProfileBackground`: an uploaded image, GIF or short video, a built-in preset or
  one or two colors); all grow `profile_version`. `Me` gains the required `time_zone`.
- `PATCH /me` accepts `pronouns`, `birthday` and `time_zone`.
- `PUT`/`DELETE /me/music`, `PUT`/`DELETE /me/background`, `PUT`/`DELETE /groups/{chat_id}/background`.
  `Group` gains the required `background`.
- `UploadPurpose` gains `profile_music` (cut to at most 30 s with the new `start_ms` and `end_ms`, tags in the new
  `Attachment.tags`) and `profile_background`.
- `PrivacySettings` gains the required `birthday_visibility`; `birthday_visibility` is a new privacy exception key.
- `NotificationPreferences` gains the optional `birthday_reminders`.

## 2.1.0-alpha.28

- `MediaKind` gains `voice` (voice messages only) and `link` (messages whose text or caption contains a web link) for
  `GET /chats/{chat_id}/media`.
- `GET /groups/{chat_id}/participants` (`GroupMemberPage`): members page by page, owner first, then admins, moderators
  and members by display name, with an optional `q` filter and the matching `total`.
- `GET /users/{user_id}/common-groups` (`CommonGroupPage` of `CommonGroup`): the groups the caller shares with the
  user.

## 2.1.0-alpha.27

- `User` gains the required `profile_version` (grows with username, display name, bio or avatar changes, not with
  presence) and `StickerPack` the required `version` (grows with title or sticker changes).
- `GET /me`, `GET /users/{user_id}`, `GET /groups/{chat_id}`, `GET /stickers/packs` and `GET /stickers/packs/{pack_id}`
  send an `ETag` with `Cache-Control: private, no-cache` and answer `304` to a matching `If-None-Match`.
- `/attachments/{attachment_id}/content` is cached as immutable (an attachment never changes).
- New server WebSocket event `user_updated` (`user_id`, `profile_version`) for users who share a chat with the user.

## 2.1.0-alpha.26

- `DELETE /chats/{chat_id}` takes `scope=me|everyone` (default `everyone`, as before). `me` deletes the direct chat
  only for the caller: it leaves their list, its history so far stays hidden for them, and it comes back with only
  newer messages when one arrives.
- `POST /chats/{chat_id}/clear` (`ClearHistoryRequest`, `scope: me|everyone`) clears the history up to the newest
  message: for the caller only, or for every member (direct chats: either user; groups: owner and admins, else
  `403`).
- New server WebSocket event `history_cleared` (`before_message_id`, `scope`, `actor_id`).

## 2.1.0-alpha.25

- Archive: `PUT` and `DELETE /chats/{chat_id}/archive` archive a chat for the caller and move it back. `Chat` gains
  the required `is_archived`; `GET /chats?archived=true|false` lists only archived or only other chats (without the
  parameter, every chat as before). Archiving unpins; an archived chat stays archived when new messages arrive;
  pinning a chat unarchives it.
- Pin order: `Chat` gains the required, nullable `pin_position` (lowest first) and pinned chats are listed in that
  order. A newly pinned chat goes to the top. `PUT /chats/pins/order` (`PinOrderRequest`) sets the order of all
  pinned chats at once.
- Saved Messages: `POST /chats` accepts the caller's own `user_id` and opens a direct chat whose only member and
  `peer` is the caller.

## 2.1.0-alpha.24

- `PrivacySettings` gains the required `last_seen_visibility`, also accepted by `PATCH /me/privacy` and as a
  `setting_key` of `PUT /me/privacy/exceptions/{setting_key}/{effect}`. It decides who sees `User.last_seen` and the
  `last_seen` of `presence` events; `presence_visibility` now decides only `User.is_online` and online `presence`
  events. Existing users start with both settings and their exceptions equal to their old `presence_visibility`.
- The `action` of the client and server `typing` events gains `choosing_sticker`, `uploading_photo`,
  `uploading_video` and `uploading_file`.

## 2.1.0-alpha.23

- `MediaKind` gains `file`: `GET /chats/{chat_id}/media?kind=file` lists messages with file attachments (documents,
  archives and other files that are not images, videos, audio or voice messages), newest first.
- Rejected sends are kept for the sender: a send refused with `forbidden`, `blocked_by_user`, `rate_limited` or
  `internal_error` is listed by `GET /chats/{chat_id}/unsent` (`UnsentMessage`, oldest first) on all of the sender's
  devices until a send with the same `client_temp_id` succeeds or `DELETE /chats/{chat_id}/unsent/{client_temp_id}`
  discards it.

## 2.1.0-alpha.22

- `Chat` gains the required, nullable `first_unread_message_id` (the oldest message the caller has not read, `null`
  when `unread_count` is 0) and `last_read_message_id` (the caller's read marker), also in `chat_created` and
  `chat_list_update`. Clients open a chat around `first_unread_message_id` and show the "new messages" divider there.
- `q` of `GET /chats/{chat_id}/messages/search` and `GET /search/messages` takes up to 256 characters (was 128).
- `RegisterRequest.display_name` is optional; a missing, empty or blank display name becomes the username.

## 2.1.0-alpha.21

- User sticker packs: `POST /attachments` takes `purpose: sticker` (PNG, JPEG or still WebP up to 5 MB, stored as a
  512 x 512 WebP of at most 512 KB). `POST /stickers/packs` creates a pack; `PATCH` and `DELETE
  /stickers/packs/{pack_id}` rename and delete it; `POST /stickers/packs/{pack_id}/stickers`, `PATCH` and `DELETE
  /stickers/packs/{pack_id}/stickers/{sticker_id}` and `PUT /stickers/packs/{pack_id}/order` manage its stickers.
  Only the owner may change a pack (`403`). Limits: 50 packs per user and 120 stickers per pack
  (`409 limit_reached`).
- `GET /stickers/packs?owned=true` lists the packs the user made. `GET /stickers/links/{slug}` opens a pack by its
  share link. A user pack is reachable by its ID (`GET`, add, report, sending its stickers) only for its owner and
  for users who opened its link; `Sticker.pack_slug` carries the link, so receivers of a sticker can open its pack.
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
