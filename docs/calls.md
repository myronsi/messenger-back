# Audio calls

One-to-one and group audio calls for the web app and the installed PWA, end-to-end encrypted, through a
self-hosted [LiveKit](https://docs.livekit.io/) media server on the same host as the API. The plan and its decisions
are summarised in [Decisions](#decisions); the encryption design is the ADR [calls-e2ee.md](calls-e2ee.md).

```
browser ──REST/WS──► api (calls service) ──► PostgreSQL (calls, participants, identities, envelopes)
   │                       │  ▲                    Redis pub/sub (call_updated, call_keys)
   │                       │  └── webhook ◄──┐
   │                       └── room API ─────► LiveKit (SFU, embedded TURN)
   └──────── WebRTC (UDP / TURN-TLS), encrypted frames ────────┘
```

The API decides who may ring, join and hear whom. LiveKit only forwards encrypted audio frames; it never holds a
media key.

## Decisions

| Topic | Decision (2026-11-07) |
|---|---|
| Platforms | Web and installed PWA only. Calls are hidden in the Capacitor shell; native push is not used for calls. |
| Size | Up to 10 participants per call (`CALLS_MAX_PARTICIPANTS`; technically at most 65, see [Limits](#limits)). |
| Hosting | The existing EC2 host, one LiveKit node, no other infrastructure. No regional restrictions; all media flows through the host's region. |
| Who can call | Direct calls only inside an existing direct chat; group calls by any current member. Each user can turn calls off (`calls_enabled` in privacy settings) for direct and group calls: nobody rings them, and a group call does not list them until they join it themselves, so other members can tell that their calls are off. |
| Blocks | Like messages: a block (either direction) stops direct calls; it does not stop either user in a shared group call. |
| End for everyone | Initiator; both members of a direct call; group owner, admins and moderators. |
| Timeline | One `call` system message per call, posted when the call ends. |
| Retention | Call metadata 90 days; key envelopes until the call ends. |
| Crypto review | No external review; internal review against the ADR's threat model. Text never claims an audit. |
| AWS/DNS | The project owner applies Security Group, DNS and certificate changes ([Deployment](#deployment)). |

## Lifecycle

A chat has at most one call that is not `ended`. A user is joined to at most one call, with one session.

```
start ──► ringing ──(first join by someone else)──► active ──(last participant gone + grace)──► ended
              │                                       │
              ├─ ring timeout, nobody answered ──► ended (missed)
              ├─ initiator leaves first ─────────► ended (cancelled)
              ├─ direct peer declines ───────────► ended (declined)
              └─ end for everyone / media failure ► ended (completed / failed)
```

Participant states: `ringing` → `joined` | `declined` | `missed`; `joined` → `left`. In a group call `declined`,
`missed` and `left` participants may join again while the call is not over.

- **Start** (`POST /chats/{id}/call`): checks membership, the server switch, privacy and blocks (direct chats),
  then creates the call, rings every other member who has calls turned on, and returns it. The caller joins with
  `POST /calls/{id}/join`. A second start while a call runs returns that call (`200`).
- **Ringing** lasts `CALLS_RING_TIMEOUT` (30 s). The worker turns expired invitations into `missed`. A direct call,
  or a group call nobody but the initiator joined, then ends as `missed`.
- **Join**: needs a registered call identity for the session, room below the size limit and below the server-wide
  admission limit. Joining from a second session is refused unless `takeover` is set; takeover removes the old
  session from the media room. Every join, leave, removal, takeover and identity change increases the **epoch**.
- **Decline** stops ringing on all of the user's devices; in a direct call it ends the call as `declined`.
- **Leave**: a direct call ends when either side leaves (`completed`, or `cancelled` before an answer), also when
  that side never joined (for example the caller whose own join failed). A group call keeps running while someone
  is joined; once the room is empty for `CALLS_EMPTY_GRACE` (20 s) it ends.
- **End for everyone**: allowed roles above; removes everyone from the media room.
- **Removal from outside**: session revoked or logged out, user removed from or leaving the group, chat deleted, or
  a block in a direct chat. The affected session is removed from the media room right away (not only when its token
  expires) and the epoch increases, so it cannot decrypt later audio.
- **Media service events** (signed webhooks) reconcile the state: a participant that disconnected from LiveKit
  for longer than the grace becomes `left`; a room the media service finished ends the call (`failed` if people
  were still connected to it). The worker also reconciles calls stuck after an API or media restart.

Every change bumps the call's `version` and is published as a `call_updated` event to all chat members. Clients
load `GET /calls` after every reconnect.

## Encryption

See [calls-e2ee.md](calls-e2ee.md). In short: each session registers a P-256 identity key
(`PUT /me/call-identity`); on every epoch each joined session seals a fresh sender key for every other joined
session with HPKE Auth and posts the envelopes to `POST /calls/{id}/keys`; the server relays them as `call_keys`
events to the recipient session only, and keeps them until the call ends for `GET /calls/{id}/keys`.

## Media grants

`POST /calls/{id}/join` and `POST /calls/{id}/grant` return a LiveKit access token (HS256 JWT signed with
`CALLS_API_SECRET`) valid for `CALLS_TOKEN_TTL` (10 min):

| Claim | Value |
|---|---|
| `iss` | `CALLS_API_KEY` |
| `sub` | identity `<user_id>:<session_id>` |
| `nbf`, `exp`, `jti` | now, now + TTL, random |
| `video` | `room: "call-<call_id>"`, `roomJoin: true`, `canPublish: true`, `canSubscribe: true`, `canPublishData: false`, `canPublishSources: ["microphone"]`, `canUpdateOwnMetadata: false` |

The token is only issued to the session that joined, only while the call is not over. A token alone does not keep
someone in a call: removals call LiveKit's `RemoveParticipant`, and a removed identity cannot get a new grant.

The server talks to LiveKit's room API (`CALLS_API_URL`, Twirp JSON: `CreateRoom`, `RemoveParticipant`,
`DeleteRoom`) with short-lived admin tokens, and receives webhooks at `POST /api/v2/calls/media-webhook`
(outside the public contract), verified with the API key and secret (`Authorization` JWT carrying the body's
SHA-256) and processed idempotently by event id.

## Push

Call invitations do not use the message notification queue (it waits five seconds and retries for fifteen
minutes). When a participant starts ringing, the API sends a web push right away, once per web destination of that
user, with `TTL` equal to the remaining ring time, `Urgency: high` and `Topic: call-<call_id>`. When ringing stops
(answered, declined, missed, ended) it sends a `cancel` push with the same topic. Native (FCM/APNS) destinations are
skipped. Chat mutes do not silence calls; `calls_enabled = false` stops ringing entirely.

Payload (JSON, alongside the message payload fields the worker already knows):

```json
{
  "type": "call",
  "action": "ring",
  "call_id": "7217400317439950100",
  "chat_id": "77",
  "user_id": "1002",
  "kind": "direct",
  "title": "Alice",
  "preview": true,
  "sender_id": "1001",
  "avatar_token": "…",
  "expires_at": 1762516830
}
```

`action` is `ring` or `cancel`; `title` is the caller (direct) or group name when the user's previews are on and
`"Messenger"` otherwise; `expires_at` is the end of the ring window in unix seconds, the same unit as message pushes. The service worker shows one
notification per call (tag `call-<call_id>`), closes it on `cancel` or expiry, and on click opens the chat, where
the app reloads the call and offers Join only if it still rings.

## Limits

| Setting | Default | Meaning |
|---|---|---|
| `CALLS_ENABLED` | `false` | Master switch. Off: calls are hidden (`GET /calls/config`), every other call operation answers `503 calls_unavailable`, messaging is unaffected, and calls still open end as `failed` ([Sweeper](#implementation)). |
| `CALLS_PUBLIC_URL` | — | `wss://` URL browsers use for LiveKit. |
| `CALLS_API_URL` | — | URL the API uses for LiveKit's room API (`http://livekit:7880` in compose). |
| `CALLS_API_KEY`, `CALLS_API_SECRET` | — | LiveKit API key and secret (secret at least 32 characters). |
| `CALLS_MAX_PARTICIPANTS` | `10` | Joined sessions per call, 2 to 65: a session seals its sender key for every other joined session in one request of at most 64 envelopes. The product limit stays 10. |
| `CALLS_MAX_ACTIVE_PARTICIPANTS` | `50` | Joined sessions across all calls on this host (admission control; `503 calls_unavailable` above it). Raise it from load-test results; the target is 250. |
| `CALLS_RING_TIMEOUT` | `30s` | Ring window. |
| `CALLS_EMPTY_GRACE` | `20s` | How long an empty group call, or a disconnected participant, may reconnect. |
| `CALLS_TOKEN_TTL` | `10m` | Media grant lifetime. |

`CALLS_ENABLED=true` with any of the URL, key or secret missing is a configuration error: the API refuses to start
and the deploy preflight fails. All of them empty with `CALLS_ENABLED` unset leaves calls off.

Rate limits per user: 10 call starts per minute, 30 joins per minute, 60 key posts per 10 s, 10 identity
registrations per minute (each change increases the epoch of the user's call), 30 grant renewals per minute.

TURN capacity: the relay range 50004-50100 has 97 ports and each TURN user holds about two, so about 48 people
can use TURN at the same time. Most users connect directly over UDP or ICE/TCP and need no relay. If the load test
or the device checklist shows more TURN users, widen `CALLS_TURN_RELAY_PORT_END` and the Security Group rule
together.

## Implementation

| Package | Role |
|---|---|
| `internal/calls` | the service: start, join, grant, decline, leave, end, identities, key envelopes, the removal hooks, the sweeper, call pushes, the LiveKit room client and the webhook handler |
| `internal/store/postgres` (`calls.go`, `queries/calls.sql`) | `CallRepository`: every state change runs in one transaction with `SELECT … FOR UPDATE` on the call row (`Update`); envelopes are written under `FOR SHARE`, so the epoch cannot move meanwhile |
| `internal/httpapi/call_server.go` | the REST operations of the `calls` tag, rate limits, error codes, `PresentCall` |
| `internal/realtime` | `Fanout.CallUpdated` / `Fanout.CallKeys`; the session filter of `call_keys` ([realtime.md](realtime.md)) |
| `cmd/api` (`calls.go`) | wiring; the sweeper runs in every API instance, with calls switched off the closer ([Sweeper](#implementation)) |
| `cmd/worker` | retention, also while calls are switched off: ended calls after 90 days, start requests after 24 h, leftover envelopes, identities of ended sessions |

**Tables** (migration `000025_calls`, see [postgres-schema.md](postgres-schema.md)): `calls` (one row per call;
`calls_one_open_per_chat` allows one call per chat that is not ended; `chat_id` turns `NULL` when the chat is
deleted, and the sweeper then ends and deletes the call), `call_participants` (one row per user and call with the
joined session and a snapshot of its identity key; `call_participants_one_joined` allows one joined row per user
across all calls), `call_identities` (one public key per session, deleted with the session or by the hooks),
`call_key_envelopes` (current and previous epoch only, deleted when the call ends), `call_start_requests`
(idempotency of `client_request_id`, 24 h) and `user_privacy_settings.calls_enabled`. `calls.summary_message_id`,
`summary_claimed_at` and `summary_posted_at` track the system message ([System message](#implementation)).

**Rules the code adds to the lifecycle above:**

- The initiator's row starts as `left` without `joined_at` until the initiator joins (the contract has no "calling"
  state), and stays so if their join fails; clients tell "started, not joined yet" from "left" by `joined_at`
  being `null`. `GET /calls` also lists calls the user started and has not joined yet.
- `version` increases with every change visible in the contract; the media bookkeeping (a participant lost by the
  media server) is stored without a new version.
- Joining with the same session again is idempotent (same epoch). Joining while joined to another call is `409
  already_in_call`; with `takeover` the user first leaves the other call. The body of `POST /calls/{id}/join` is
  optional; an empty body counts as none, also with `Content-Type: application/json`.
- Start and join check membership, blocks and the session's identity once before the transaction (fast refusal)
  and again inside it, under locks that serialise them with removals: start reads the members to ring with `FOR
  KEY SHARE` on their `participants` rows and builds the ring list (privacy, blocks) in the same transaction as the
  insert; join takes `FOR KEY SHARE` on the user's `participants` row, then the call row, then `FOR SHARE` on the
  session's `call_identities` row, and re-checks blocks of a direct call. A member removed or a session ended at the
  same time either makes the join fail (`404`, `422 call_identity_required`) or waits until it committed, so the
  `MembersRemoved` / `SessionsEnded` hooks find the participant and remove it. A replayed `client_request_id`
  is answered only to a current member of the chat.
- Join creates the media room (idempotent `CreateRoom`) before it changes anything, so a failing media service
  answers `503` and leaves the user where they were (also in the other call with `takeover`).
- Leave works for the user, from any of their sessions. Decline, leave and end of a call that is over change nothing
  (`204`).
- A removed session or member of a direct call ends it; in a group call the participant leaves (`epoch` + 1).
- The server-wide admission (`CALLS_MAX_ACTIVE_PARTICIPANTS`) counts joined rows before the join transaction, so
  concurrent joins can pass it by a few; the per-call limit is checked under the row lock.
- Every call operation except `GET /calls/config` may answer `503` (`CallsUnavailable` in the contract):
  `calls_unavailable` when calls are off or at capacity (`Retry-After: 10`), `internal_error` with `Retry-After: 2`
  when LiveKit or a store is away.
- After commit: `call_updated` to the members (members cache), pushes, LiveKit calls and the system message run in
  the background with their own deadline; shutdown waits for them.

**LiveKit** (protocol of server `v1.13.10`, without `server-sdk-go`):

- Grants: HS256 JWT signed with `CALLS_API_SECRET`, claims as in [Media grants](#media-grants).
- Room API: Twirp JSON, `POST {CALLS_API_URL}/twirp/livekit.RoomService/{CreateRoom|RemoveParticipant|DeleteRoom|ListParticipants}`
  with an admin token (`video.roomAdmin`, `roomCreate`, `roomList`, `room`), valid one minute. Twirp `not_found` on
  removal or deletion counts as done. Responses are never logged (`CreateRoom` returns TURN credentials).
- LiveKit runs with `room.auto_create: false`: every join (before the join is stored) and every grant renewal
  calls `CreateRoom` (`empty_timeout` 120 s, `departure_timeout` = `CALLS_EMPTY_GRACE`, `max_participants` =
  `CALLS_MAX_PARTICIPANTS`); if that fails, the request answers `503` and the client retries.
- Webhook `POST /api/v2/calls/media-webhook` (router, outside the contract and the bearer authentication): the
  `Authorization` header is a JWT (with or without `Bearer `) signed with the API secret, `iss` = API key, `exp`
  required, claim `sha256` = base64 (standard) of the SHA-256 of the raw body. The webhook arrives over plain HTTP
  inside the compose network, so this signature is the only authentication. Events are deduplicated by `id` in Redis
  (`once:calls_webhook:{digest}`, 24 h; released when handling failed so LiveKit's retry is processed).
  `participant_joined` clears a disconnect, or removes an identity that is not a joined session of the call;
  `participant_left` / `participant_connection_aborted` mark the participant as disconnected (it leaves after the
  grace); `room_finished` ends the call (`failed` if a joined participant was still connected). Other events are
  ignored.

**Sweeper** (`calls.Service.Run`, every API instance): every 2 s it ends or updates due calls (ring timeout, empty
grace, participants disconnected longer than the grace, deleted chats, calls older than 24 h) and deletes ended calls
of deleted chats. A second goroutine runs every 30 s, with a 30 s deadline per run, so slow LiveKit answers never
delay ring timeouts: it compares joined participants with `ListParticipants` of their room (3 s per room),
re-posts lost system messages and updates the participant gauge. Transitions take the row lock, so several
instances can sweep at once. It runs in the API rather than the worker because ending a call posts a system message
(Snowflake ids) and publishes events, which only the API's realtime stack can do.

**Calls switched off** (`calls.Service.RunDisabled`, every API instance with `CALLS_ENABLED=false`): right after
start and every 30 s it ends every call that is still open as `failed` (removing everyone, publishing
`call_updated`), posts their system messages and retries lost ones, and deletes ended calls of deleted chats. The
worker's retention runs whatever the switch says, so these calls are purged after 90 days like any other. Switching
calls off therefore ends running calls within about 30 s of the deploy; instances must not run with different
switches for longer than a rolling restart.

**System message** (at least once, without duplicates): the poster claims the call with a one-minute lease
(`summary_claimed_at`) and fixes the message id on the first claim (`summary_message_id`, a Snowflake id from the
message service); only after the message was stored does it set `summary_posted_at`. A failed send releases the
lease; a crash leaves it to expire, and the 30 s run retries calls ended in the last hour with the same message id.
ScyllaDB writes a message with the same id and timestamp as an upsert, so a retry after a crash between storing and
marking stores no second message (the realtime `message_created` event may repeat for that id).

**Push:** `notifications.Sender.SendCall` (web only; `TTL` = remaining ring time, at least 30 s for `cancel`;
`Urgency: high`; `Topic: call-<id>`), at most 8 at a time per call, within one 15 s deadline. Destinations come from
`notification_devices` of active sessions; disabled notification preferences skip the user; previews decide the
title. Payload contents are never logged.

**Metrics:** `messenger_calls_started_total`, `messenger_calls_ended_total{reason}`,
`messenger_call_participants_active`, `messenger_call_join_failures_total{code}`,
`messenger_call_webhook_events_total{event,outcome}`. Keys, envelopes, tokens and push payloads are never logged.

**Rate limits** (Redis GCRA, per user): `call_start` 10/min, `call_join` 30/min, `call_keys` 60 per 10 s,
`call_identity` 10/min, `call_grant` 30/min.

**Tests:** `internal/calls` (state machine, grants, webhook signatures, the Twirp client, and against PostgreSQL and
Redis: authorization, privacy and blocks, concurrent starts, answers from two sessions, end racing join, join
racing a member removal or session end, start racing a removal, replay after removal, the media room before the
join, idempotency, ring timeout, epochs, `stale_epoch`, roster checks, removal hooks, retention, the system message
after a crash, calls switched off, webhooks),
`internal/httpapi/call_server_test.go` (contract-checked HTTP flow, calls switched off),
`internal/realtime/call_events_test.go` (event schemas, session filter), `internal/config/calls_test.go`.

## Deployment

How LiveKit runs next to the API on the existing EC2 host. Enabling, disabling and rotating credentials are in
[deploy-go.md](deploy-go.md#audio-calls); the resource budget and alerts in
[deployment.md](deployment.md#audio-calls-media-server).

```
browser ──wss://calls.<domain> (443)──► nginx ──► 127.0.0.1:7880 ─┐
        ──UDP 50000-50003 (ICE), TCP 7881 (ICE/TCP)─────────────► │ livekit (compose service, profile calls)
        ──TURN: UDP 3478, TLS turn.<domain>:5349 (or 443)───────► │ embedded TURN, relay UDP 50004-50100
api ────http://livekit:7880 (room API)──────────────────────────► ┘
livekit ─http://api:8080/api/v2/calls/media-webhook (signed)────► api
```

- **Service:** `livekit` in `deploy/go/compose.yaml`, image `livekit/livekit-server:v1.13.10`, profile `calls`.
  `deploy.sh` starts it only with `CALLS_ENABLED=true` (and removes it otherwise), so hosts without calls are
  unaffected. One node, no Redis.
- **Configuration:** `deploy/go/livekit/livekit.yaml` holds what is the same everywhere: rooms are created only by
  the API (`room.auto_create: false`, so the API calls `CreateRoom` before the first grant), `room.max_participants`
  10 (overridden by `CALLS_MAX_PARTICIPANTS`), `room.empty_timeout` 120 s, `room.departure_timeout` 20 s
  (`CALLS_EMPTY_GRACE`), the webhook to the API, JSON logs at `info`, Prometheus on 6789. Compose adds the
  per-host values from `.env`: `LIVEKIT_KEYS` from `CALLS_API_KEY`/`CALLS_API_SECRET` (the webhook is signed with
  the same key), `NODE_IP` from `CALLS_NODE_IP`, the ports, and the TURN domain and certificate. LiveKit gets only
  these, never the whole `.env`.
- **Networking:** a bridge network with explicit ports, each the same on the host and in the container (LiveKit
  advertises them). ICE over UDP is multiplexed over four ports (at least one per vCPU), so the number of open ports
  does not grow with participants. Signalling (7880) and metrics (6789) are published on 127.0.0.1 only.
- **Limits:** at most `CALLS_CPUS` (1.0) vCPU with half the CPU weight of the other containers, `CALLS_MEM_LIMIT`
  (1 GiB) memory, and LiveKit refuses new participants above about 160 Mbit/s. The API's admission limit
  (`CALLS_MAX_ACTIVE_PARTICIPANTS`) applies first.
- **Health:** the container's health check and `deploy.sh` both request `http://127.0.0.1:7880/`, which answers `OK`.

### TURN/TLS and port 443

Some networks (corporate, hotel, some VPNs) allow nothing but TCP 443. TURN over TLS carries media there; nginx's
http module cannot, because it only proxies HTTP.

- **Default: TURN/TLS on 5349.** LiveKit listens on 5349 with its own name `turn.<domain>` and certificate. It
  needs one more Security Group rule and reaches every network that allows outgoing TCP to ports other than 443.
- **Optional: TURN/TLS on 443.** nginx's `stream` module reads the TLS server name without decrypting: `turn.<domain>`
  goes to LiveKit, everything else to the existing HTTPS sites, which move from 443 to `127.0.0.1:8443` and receive
  the client address through the PROXY protocol. Example and steps:
  `deploy/go/livekit/nginx-turn-443-stream.conf.example`; in `.env`: `CALLS_TURN_TLS_PORT=443`,
  `CALLS_TURN_TLS_PUBLISH=127.0.0.1:5349`. Use it when the device checklist finds users on 443-only networks. It
  touches every HTTPS site of the host, so change it in a maintenance window and test the web app right after.
- **Signalling** (`wss://calls.<domain>`) always goes through the existing nginx as an ordinary WebSocket reverse
  proxy: `deploy/go/livekit/nginx-calls.conf.example`. It logs paths without the query string (browsers send the
  access token there) and does not expose LiveKit's room API (`/twirp/`).

### AWS and DNS checklist

The project owner applies these by hand; nothing in the repository changes AWS. Everything uses the instance's
existing Elastic IP: no new IP, load balancer or instance.

**Security Group (inbound, IPv4 `0.0.0.0/0`):**

| Protocol | Port(s) | Purpose | When |
|---|---|---|---|
| UDP | 50000-50100 | ICE media (50000-50003) and TURN relay ports (50004-50100) | always |
| TCP | 7881 | ICE over TCP, when UDP is blocked | always |
| UDP | 3478 | TURN over UDP (and STUN) | always |
| TCP | 5349 | TURN over TLS | default mode; not with TURN on 443 |
| TCP | 443 | already open: web app, API, `wss://calls.<domain>`, and TURN/TLS in the 443 mode | exists |
| TCP | 80 | already open for certificate renewal (HTTP-01) | exists |

Never open 7880 (signalling, behind nginx), 6789 (metrics), 8080/8081 (API, worker) or 8443/5350 (internal in the
443 mode). The TURN relay ports must also accept traffic from the Elastic IP itself, which `0.0.0.0/0` covers:
LiveKit's TURN reaches its own media ports through it.

**DNS** (the zone of the web app's domain), TTL 300 while testing:

| Record | Type | Value |
|---|---|---|
| `calls.<domain>` | A | the Elastic IP |
| `turn.<domain>` | A | the Elastic IP |

No AAAA records: the media server advertises IPv4 only.

**Certificates** (publicly trusted; browsers reject self-signed ones for TURN/TLS):

- `calls.<domain>`: through nginx like the existing sites (`certbot certonly --nginx -d calls.<domain>`).
- `turn.<domain>`: certbot with the deploy hook from [deploy-go.md](deploy-go.md#enabling-calls-on-a-host), which
  copies it where the deploy user can read it and restarts LiveKit after each renewal.

**Frontend:** its Content-Security-Policy needs `wss://calls.<domain>` and `https://calls.<domain>` in `connect-src`
(messenger-front `deploy/nginx/security-headers.conf`).

### Traffic budget

Planning numbers for 32 kbit/s Opus at 20 ms frames (50 packets/s per track), without packet headers:

| Profile | Forwarded tracks | Outbound | Packets/s out | Egress per hour |
|---|---|---|---|---|
| 25 rooms × 10 people (each hears 9) | 25 × 10 × 9 = 2,250 | ≈ 72 Mbit/s | ≈ 112,500 | ≈ 32 GB |
| 125 direct calls (each hears 1) | 250 | ≈ 8 Mbit/s | ≈ 12,500 | ≈ 3.6 GB |

Inbound is one track per participant (250 × 32 kbit/s ≈ 8 Mbit/s, 12,500 packets/s). On the wire, IP, UDP, RTP and
SRTP headers and the end-to-end encryption trailer add roughly half again (≈ 110 Mbit/s and ≈ 50 GB per hour for the
first profile). Media of TURN users passes the host's network twice (client to TURN, then TURN to the media ports
through the Elastic IP), so it counts twice in the network metrics. On two burstable vCPUs, packets per second is
the likely first limit, before bandwidth.

**Price it before launch:** the egress of the first profile and of a realistic day (the expected call minutes per
day) at the region's current data-transfer-out rate, plus CPU credits if the instance runs in unlimited mode. Set
an AWS Budgets alert on monthly data transfer. These are planning estimates, not a bill or a measured capacity.

### Load test

Never on the production host without a controlled window and abort thresholds, and staging first.

1. **Staging first**, with the same stack and limits. Record a baseline of the host for 30 minutes of normal load:
   CPU, `CPUCreditBalance`, packets/s, `docker stats`, API p95 latency.
2. **Window:** a low-traffic hour, announced to the team. One person watches the abort signals and stops the test,
   another runs it. Have the switch-off ready ([deploy-go.md](deploy-go.md#disabling-calls-rollback)).
3. **Load generator:** LiveKit's CLI (`lk load-test`; see `lk load-test --help`) on machines outside the host, with
   speech-like audio from every publisher (overlapping talkers, not silence), plus two or three real browsers with
   end-to-end encryption to judge quality. Rooms must exist first (`room.auto_create` is off): create them on the
   host with `lk room create` against `http://127.0.0.1:7880`. Use the staging key, never production's on a laptop.
4. **Ramp,** 5 minutes per step, checking between steps: 2, 5, 10, 15, 20, 25 rooms of 10; then 25, 60, 125 direct
   (2-person) rooms; then a mix (10 rooms of 10 and 75 direct calls). Finish with a 30-minute soak at the highest
   step that passed, and one run with a TURN/TLS-forced client.
5. **Abort at once** (stop the generators) when any of these holds for 2 minutes:
   - the API's `/readyz` fails, its p95 latency is above twice the baseline, or message sending errors rise;
   - host CPU is above 90 %, or `CPUCreditBalance` is below 30 % of its maximum or falling fast enough to reach that
     within the hour;
   - `livekit_node_packet_total{type="dropped"}` is above 1 % of `type="out"`, or the median `livekit_quality_score`
     is below 3.5;
   - `livekit` memory is above 90 % of `CALLS_MEM_LIMIT`, or the host has less than 1 GiB available.
6. **Result:** set `CALLS_MAX_ACTIVE_PARTICIPANTS` to about 70 % of the highest participant count that passed, and
   write the measured CPU, packets/s, egress, join failures and reconnect time per step into the release ticket.
   250 stays a target until a step shows it.
7. **Clean up:** delete the test rooms; if a key was used outside the host, rotate it.

### Physical device checklist

Run it before enabling calls for users, after LiveKit or SDK upgrades, and after changing TURN, nginx or the
Security Group. Two testers with two accounts; record every run (device, OS and browser versions, network, result,
notes) in the release ticket.

**Devices and browsers:**

- Desktop: Chrome, Edge and Firefox (Windows or macOS), and Safari (macOS), current versions.
- Android: Chrome in a tab, and the installed PWA.
- iOS: Safari in a tab, and the home-screen PWA.
- The Capacitor app (Android/iOS shell) shows no call buttons at all.

**Networks** (every device at least on the first two):

- Home Wi-Fi.
- Cellular (4G/5G, Wi-Fi off).
- A VPN (company or commercial).
- Forced TURN/TLS: block outgoing UDP and TCP 7881 in the test laptop's firewall (or use a network that allows only
  TCP 443). In Chrome, `chrome://webrtc-internals` shows the selected candidate pair as `relay` over TLS.
- Long distance: at least one call each between the host's region and the US, South America and Asia (or as far
  away as testers are), noting the round-trip time from `webrtc-internals` and how the audio sounds.

**In each run the tester should see:**

1. Ringing: the other side rings on all its signed-in web devices within a few seconds. With the app closed a
   notification appears (best effort on web and PWA); opening it after the call ended offers no Join.
2. Answering: audio both ways within 5 seconds after the second user accepted and allowed the microphone; the other
   devices stop ringing.
3. The security code is identical on both screens and changes when someone joins or leaves a group call.
4. Mute: the other side hears nothing; mute survives a short network drop (Wi-Fi off for 5 seconds, then on), and
   the call reconnects by itself.
5. Group: up to 10 people hear each other; the 11th is refused with a clear message; someone removed from the group
   drops out of the call at once.
6. Leaving: the browser's and the OS's microphone indicator turns off; the chat shows one call entry with the right
   status (completed, missed, declined) and duration.
7. Lock and background: lock the phone, or switch apps, for 30 seconds during a call and note whether audio continues
   in both directions. Android Chrome, the Android PWA, iOS Safari and the iOS home-screen PWA behave differently:
   record it, do not expect native-call behaviour. After unlocking, the call is still there or offers to rejoin.
8. A denied or missing microphone and an unsupported browser show an explanation, never an unencrypted call.

### Local development

`compose.dev.yaml` has a `livekit` service in the profile `calls`, configured by `deploy/dev/livekit/livekit.yaml`:
no TURN, every port on 127.0.0.1 (7880 signalling, 7881 ICE/TCP, UDP 50000-50001), and development keys (`devkey`
and a secret marked `dev-only`) unless `.env` sets `CALLS_API_KEY` and `CALLS_API_SECRET`. Set `CALLS_ENABLED=true`
in `.env` and run `docker compose -f compose.dev.yaml --profile calls up -d`. Where the container runtime does not
forward UDP to the host (some Windows and macOS setups), media falls back to TCP 7881. `deploy.sh` refuses a secret
containing `dev-only`.
