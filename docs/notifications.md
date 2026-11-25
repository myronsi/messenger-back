# Message notifications

The Go API stores account preferences, timed chat mutes and session-bound push destinations in PostgreSQL
(migration `000013`). New messages and forwards commit a notification intent **before** writing the message to
ScyllaDB. If committing the intent fails, sending fails before the message is written. As soon as ScyllaDB confirms
the write (`LOCAL_QUORUM`), the API marks the intent due and wakes the worker in one statement (`pg_notify` on the
`notification_jobs` channel). The worker holds one connection that `LISTEN`s on that channel and claims at once, so
the push leaves within milliseconds of the save. Every intent still starts five seconds in the future: if the API
dies between the write and the wake-up, the worker picks it up after those five seconds, and a lost wake-up is
caught by the once-a-second poll. The worker verifies that the message exists, and checks membership, join time,
blocks, hidden messages, read markers, preferences, mutes and live-device activity before delivery. A failed Scylla
write never marks the intent due and cannot produce a phantom alert. Marking due only touches an intent that no
worker has claimed yet. The wake-up needs a direct PostgreSQL connection: a pooler in transaction mode drops
`LISTEN`; the worker then still delivers through the poll, one second later.

The queue does not depend on Redis event publishing. Each worker runs four claim loops; they claim with `SKIP LOCKED`
and a two-minute lease. One job loads its recipients and their destinations in two batch queries and sends to at
most eight destinations at a time, so a large group or a slow provider no longer holds up other chats.
Transient failures retry after the lease; intents expire after fifteen minutes or eight delivery attempts. Delivery
receipts prevent successful destinations from being resent during partial retries. A crash after provider acceptance
and before saving a receipt can still repeat a push: delivery is at least once. Web notification tags and native
collapse identifiers replace a conversation's existing notification. Push-provider acceptance does not guarantee
that the operating system displayed an alert.

Provider answers fall into four groups:

| Answer | Effect |
|---|---|
| Web 404/410, APNs 410 or `BadDeviceToken`/`Unregistered`, FCM `UNREGISTERED` | The destination is removed at once. |
| Web 400/403, APNs `DeviceTokenNotForTopic`, FCM `SENDER_ID_MISMATCH` | Closed for this message, counted on the destination (`notification_devices.failures`, migration `000052`). Three in a row remove it; a delivery resets the count. A 403 is not removed at once because a wrong server key would otherwise drop every destination. |
| Web/APNs 413 (`PayloadTooLarge`) | Closed for this message, not counted. |
| Anything else (429, 5xx, transport errors) | The job retries after the lease. A 401 (FCM, APNs) or APNs 403 also drops the cached provider token. |

The APNs signing token is reused for 40 minutes (Apple allows a new one at most every 20 minutes and accepts one for
an hour). The push lifetime (`TTL`, FCM `ttl`, `apns-expiration`) follows the payload's `expires_at`, between 30
seconds and a day: fifteen minutes for messages, twelve hours for birthday reminders.

Birthday reminders go to members of a shared direct chat at 09:00 local time. Pairs where the birthday is hidden
from the receiver are recorded as handled without a push, so they cannot fill every batch and block the receivers
after them. Message requests (either side) and chats the receiver muted get no reminder.

Only new messages are notification events. Edits, typing, reactions and read receipts do not alert. Own messages,
system messages and messages from before a member joined are excluded. Members whose chat is still in their Requests
folder ([chats.md](chats.md#requests-folder)) are left out of the intent, and a message with no recipient left queues
no intent. Previews and sound default off. The React
client suppresses alerts in the focused conversation and elects one visible browser tab for live alerts. A visible,
focused client with a healthy socket reports activity every ten seconds; its push is suppressed for 25 seconds.
Hiding or closing the page reports inactivity at once (a `keepalive` request). When a destination was skipped only
because it was active, the job is not finished: it runs once more two seconds after the latest lease ends
(`notification_jobs.deferred`, the deferral does not count as an attempt). If the message is still unread and the
destination is no longer active (tab closed, laptop asleep), it gets the push then; a destination that is still
active is done. Different devices remain independent.

## Call pushes

Call invitations do not use this queue ([calls.md](calls.md#push)). When a call starts, the API looks up the web
destinations of the rung users' active sessions (`CallRepository.PushDevices`, native destinations skipped, users with
notifications turned off skipped) and sends each one at once, in the background after the commit:
`notifications.Sender.SendCall` with `TTL` = remaining ring time, `Urgency: high` and `Topic: call-<call_id>`, so a
later push of the same call replaces it. When ringing stops (answered, declined, missed, ended) the same users get an
`action: cancel` push with the same topic. Previews decide the title (caller or group name, else `Messenger`) and add
`sender_id` and an avatar token; `expires_at` is in unix seconds. The chat id is sent as `call_chat_id`, not
`chat_id`, so older service workers, which treat any push with a numeric `chat_id` as a new message, reject call
pushes. `cancel` pushes carry `reason`: `answered`, `declined`, `missed` or `ended` ([calls.md](calls.md#push)).
Chat mutes do not apply. An expired destination is
removed like in the queue. Payloads and destinations are never logged.

## Configuration

Current setup: a web VAPID key pair has been generated into the Git-ignored root `.env.notifications` file.
Both local development and deployment Compose load an optional `.env.notifications` file beside their Compose
file, for both API and worker. The deployment workflow installs it from the encrypted `NOTIFICATION_ENV` repository
secret with mode 0600; it is intentionally excluded from Git and release bundles. Its contact URL is the repository's GitHub issues page.
Native Android push is unconfigured because there is no Firebase project. Apple push is deferred. The native
provider variables in this file are empty, so both capabilities stay disabled. In-app alerts remain available.

API and worker must receive the same optional settings. An unconfigured platform is reported unavailable by
`GET /me/notifications`; in-app alerts still work. Generate one P-256 VAPID key pair with the Web Push library's
`GenerateVAPIDKeys`, keep it stable across deployments, and provide:

```env
PUSH_VAPID_PUBLIC_KEY=<base64url public key>
PUSH_VAPID_PRIVATE_KEY=<base64url private key>
PUSH_VAPID_SUBJECT=mailto:operator@example.com
```

Serve the web app over HTTPS and deploy `notification-worker.js` alongside `sw.js`. The existing generated worker
imports the notification handlers. Account identity is kept in a separate worker cache; logout clears it, removes
visible alerts and unsubscribes. The API also excludes revoked or expired sessions even if client cleanup fails.
Subscriptions are restricted to the Google, Mozilla, Apple and Windows push endpoints listed in
`internal/notifications/sender.go`; redirects are disabled. Supporting another browser provider requires adding
its trusted HTTPS endpoint explicitly.

For Android, enable the FCM HTTP v1 API and supply a service account with permission to send messages:

```env
PUSH_FCM_SERVICE_ACCOUNT=<single-line service-account JSON, including its private_key>
```

For iOS, configure an APNs token signing key and the actual application bundle identifier:

```env
PUSH_APNS_KEY=<PEM key, with real newlines or literal \n escapes>
PUSH_APNS_KEY_ID=<key id>
PUSH_APNS_TEAM_ID=<Apple team id>
PUSH_APNS_TOPIC=com.messenger.app
PUSH_APNS_SANDBOX=false
```

Store credentials in deployment secrets, never in Git. The native project setup is documented in the frontend's
`docs/notifications.md`. Native projects are not currently checked into that repository; creating/signing them and
adding the provider configuration are required before device delivery can be verified.

## API and release order

- `GET /me/notifications`: preferences, current mutes, capabilities and the public VAPID key.
- `PUT /me/notifications`: replace `{enabled, sound, previews}`.
- `POST /me/notification-devices`: register `{platform, destination, p256dh?, auth?}`; returns a stable destination id.
- `DELETE /me/notification-devices/{id}`: unregister only a destination owned by the caller's current session.
- `PUT /me/notification-devices/{id}/activity`: `{active, client_id}`; same ownership restriction. Each browser tab has its own lease.
- `PUT /chats/{chatId}/notification-mute`: `{duration: "off" | "1h" | "8h" | "forever"}`; membership required.

- `GET /notifications/avatar/{token}` and `POST /notifications/read/{token}`: no session. The service worker uses
  them for the sender's photo and the "Mark as read" action. The tokens come in the push payload.

A message push carries `chat_id`, `message_id`, `user_id`, `sound`, `expires_at`, `content_type` (`text`, `photo`,
`video`, `audio`, `voice`, `file`, `album`, `sticker`, `poll`, `contact`, `location`), `chat_type` (`direct` or `group`) and `preview` (whether the
title and body show the sender and text). The server sends no English placeholder text: without previews `title` is
`Messenger` and `body` is empty, and the client shows a localized label for `content_type`. With previews, `title`
is the sender (the recipient's contact name for the sender when there is one, at most 64 characters), `body` is the
text or caption (at most 160 characters; for a sticker its emoji, for a poll `📊 <question>`, for a contact
`👤 @<username>`, for a location `📍` and the place name), and a group adds `chat_title` (at most 64
characters). `read_token` comes always (valid seven days) and, only with previews on, `avatar_token`
(valid one hour). In a group with a picture `avatar_token` is a group-picture token (`c.` prefix) for the same
`/notifications/avatar/{token}` route, which answers only while the recipient is still a member; otherwise it is the
sender's. With previews on, a photo, video, album, sticker or a message that is a single custom emoji also
carries `image_token` (`m.` prefix, valid one hour) for the same route. It answers with the picture the
notification shows: the display copy of a photo, the thumbnail of a video, the first item of an album, the sticker
image (the still `panel` frame of a sticker made from a video or GIF) or the custom emoji image. It answers 404 once the recipient left the chat, joined after the message, or the
message was deleted for everyone or for the recipient, and for messages without a picture (text, voice, documents). Birthday reminders carry `kind: "birthday"`, the birthday user's avatar token with previews, and their own
web push topic (`birthday-<chat_id>`) so they do not replace a pending message push. A message push to a member it mentions (`@username`, or `@all` from an owner or admin, also in a muted chat while `mentions_in_muted` is on) carries `kind: "mention"`; other message pushes have no `kind`. Each token is an HMAC bound to one purpose, recipient, push device and chat or sender, keyed from
`PUSH_VAPID_PRIVATE_KEY`, so API and worker need no extra secret. Rotating that key invalidates all tokens. A token names its device and that device's session, and works only while both
still match and the session is active. Logging out, revoking the session or registering the browser again under
a new session ends it at once. The two token routes are left out of
tracing, and request logs record only the route pattern, so tokens do not reach logs or spans.

Destinations have one owning session. Registration on the same physical destination moves it to the new session.
Each session may register at most sixteen destinations. No push destination or provider response body is logged.

Apply the additive migration and deploy API/worker before the frontend. Older server binaries can run with the
new tables left in place on rollback; do not roll the schema back while this worker is running. The notification
API is an additive contract extension; frontend notification DTOs are isolated until the updated contract package
is published and can be pinned. Existing generated frontend types remain aligned with the currently pinned
package. Publish/pin the contract as part of the coordinated release.

Monitor `messenger_notifications_total{outcome=~"delivered|retry|expired|unregistered|rejected"}` on the worker. Alert on
sustained retries/expirations and inspect `notification_jobs` for queue depth/oldest creation time. Retrying a
provider-authentication error will not repair invalid credentials.

## Verification

Unit tests cover privacy, eligibility, partial retries, parallel sending, the active-window deferral, content types,
group payloads, provider error classification, push lifetimes and the APNs token cache. HTTP tests validate
authentication, ownership and contract response shapes. PostgreSQL integration tests cover mutes, read markers,
session revocation, lease recovery, deferral, batch recipients and the failure counter; they require `TEST_DATABASE_URL`. The normal migration up/down tests include
the new tables. Before release, test actual browser pushes, denied/revoked permissions, logout, multiple tabs and
devices, foreground/background transitions, direct/group navigation after login, token rotation, and physical
Android/iOS devices with production-equivalent credentials.
