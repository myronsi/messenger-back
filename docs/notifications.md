# Message notifications

The Go API stores account preferences, timed chat mutes and session-bound push destinations in PostgreSQL
(migration `000013`). New messages and forwards commit a notification intent **before** writing the message to
ScyllaDB. If committing the intent fails, sending fails before the message is written. The worker waits five
seconds, verifies that the message exists, and checks membership, join time, blocks, hidden messages, read markers,
preferences, mutes and live-device activity before delivery. A failed Scylla write cannot produce a phantom alert.

The queue does not depend on Redis event publishing. Workers claim with `SKIP LOCKED` and a two-minute lease.
Transient failures retry after the lease; intents expire after fifteen minutes or eight delivery attempts. Delivery
receipts prevent successful destinations from being resent during partial retries. A crash after provider acceptance
and before saving a receipt can still repeat a push: delivery is at least once. Web notification tags and native
collapse identifiers replace a conversation's existing notification. Push-provider acceptance does not guarantee
that the operating system displayed an alert.

Only new messages are notification events. Edits, typing, reactions and read receipts do not alert. Own messages,
system messages and messages from before a member joined are excluded. Previews and sound default off. The React
client suppresses alerts in the focused conversation and elects one visible browser tab for live alerts. A visible,
focused client with a healthy socket reports activity every ten seconds; its push is suppressed for 25 seconds.
Closing a browser abruptly can therefore leave a short suppression window. Different devices remain independent.

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

A web push payload carries `preview` (whether the title and body show the sender and text), `read_token` (always, valid seven days) and, only with previews on, `avatar_token`
(valid one hour). Each token is an HMAC bound to one purpose, recipient, push device and chat or sender, keyed from
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

Monitor `messenger_notifications_total{outcome=~"delivered|retry|expired|unregistered"}` on the worker. Alert on
sustained retries/expirations and inspect `notification_jobs` for queue depth/oldest creation time. Retrying a
provider-authentication error will not repair invalid credentials.

## Verification

Unit tests cover privacy, eligibility, partial retries and provider error classification. HTTP tests validate
authentication, ownership and contract response shapes. PostgreSQL integration tests cover mutes, read markers,
session revocation and lease recovery; they require `TEST_DATABASE_URL`. The normal migration up/down tests include
the new tables. Before release, test actual browser pushes, denied/revoked permissions, logout, multiple tabs and
devices, foreground/background transitions, direct/group navigation after login, token rotation, and physical
Android/iOS devices with production-equivalent credentials.
