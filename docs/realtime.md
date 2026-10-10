# Realtime gateway

`GET /api/v2/ws` serves the WebSocket protocol of the contract ([api/websocket.md](../api/websocket.md)). Every browser
tab has one connection, which carries the events of all of the user's chats. The gateway runs inside every API
instance; instances share nothing but Redis ([redis.md](redis.md)).

```
client ──ws──► gateway (instance A) ──► messages.Service ──► ScyllaDB / PostgreSQL
                   ▲                         │
                   │                         ▼
            user:{id} channel ◄──── Fanout (renders per recipient) ──► Redis pub/sub ──► gateway (instance B) ──► client
```

## Packages

| Package | Role |
|---|---|
| `internal/realtime` | `Gateway` (handshake, connections, delivery rules), `conn` (read, write and ping loops), `Fanout` (renders events per recipient and publishes them), `events.go` (strict decoding of client events) |
| `internal/messages` | sending, editing, deleting, reacting, reading, resending and typing, with every authorization check; talks to the realtime layer only through `Notifier` |
| `internal/users` | users as a given viewer may see them (privacy settings and exceptions, contact names, presence) |
| `internal/httpapi/present.go` | turns domain values into the contract types, shared by REST and the gateway |

## Connecting

1. `POST /api/v2/ws/ticket` (bearer token) returns a single-use ticket valid for 30 s.
2. `wss://…/api/v2/ws?ticket=…&api_version=…`. The origin must be the API's own or one of `CORS_ORIGINS`.
3. The ticket is redeemed during the handshake (and the session checked). An invalid ticket gets close code `4401`;
   a client whose `api_version` is below `MIN_CLIENT_API_VERSION` gets `hello` and then `4426`.
4. The first connection of the user on this instance subscribes `user:{id}` and claims presence.

The HTTP server's timeouts do not apply to the upgraded connection (net/http clears the deadlines when the
connection is hijacked; a test keeps it that way), and no database connection is held per socket: every event
borrows one from the pool briefly.

## Client events

Every frame is decoded strictly (unknown fields, wrong types and oversized values are refused with
`validation_failed`, exactly as the JSON Schemas of the contract; a test runs every example through the decoder).
Frames are limited to 32 KiB. Events of one connection are handled in order, each within 10 s, and answered with
`ack` or `error` carrying the `client_temp_id`; `typing` is never answered.

Limits per user, across connections and instances (Redis GCRA): 30 messages (and resends) per 10 s with bursts of
20, 60 other actions per 10 s, one typing indicator per second (excess ones are dropped silently).

Authorization is checked for every action, against the members cache (`members:{chat_id}`, invalidated on every
membership change; the check fails closed when Redis or PostgreSQL cannot answer):

| Action | Rule |
|---|---|
| `message` | member; in a direct chat neither user blocked the other (`blocked_by_user`, which applies to every action below too; typing is dropped); a `file`/`voice` attachment is the sender's upload (voice: audio); `reply_to` is a message of the chat; idempotent per user, chat and `client_temp_id` (24 h): a retry is acknowledged only once the first send is stored |
| `edit` | the sender's own text message, not deleted; members who deleted it for themselves get no `edit` |
| `delete` | `me`: any member; `everyone`: the sender, or the owner, an admin or a moderator of a group |
| `reaction_add`/`reaction_remove` | member, message not deleted |
| `read` | member; the read position only moves forward; an older position changes nothing and is not broadcast |
| `resend` | the sender's own message; it goes to the sender's devices and to the members it was hidden from as not delivered, never to those who deleted it for themselves |
| `typing` | member |

A chat the user is not in looks like a chat that does not exist (`not_found`).

## Delivery

`Fanout` renders every event per recipient (the sender as each member may see them, the sender's own copy with
`client_temp_id`) and publishes all of them in one pipeline. What travels on `user:{id}` is a small frame around the
client event, so the receiving gateway can apply three rules before forwarding:

- **Removed from a chat:** (sent by the chat and group endpoints, #53) the removal (`chat_deleted`) marks the chat
  for that user; later events of the chat are
  dropped, including ones a concurrent send published after loading the old member list. `chat_created` or
  `group_created` for the chat lifts the mark.
- **Presence order:** a presence change is forwarded only when its version is newer than the last one for that
  user, because changes published by different instances can overtake each other.
- **Backpressure:** each connection has a bounded send buffer (`WS_SEND_BUFFER`, 256 events). A client that cannot
  keep up is closed with `1013` instead of slowing down everyone else; it reconnects and catches up through REST.

Delivery is best effort. After every reconnect the client loads what it missed with the `after` cursor; the
`event_id`s let it spot duplicates. When the bus had to subscribe again after losing Redis, the affected users'
sockets are closed with `1012` "reconnect" for exactly this reason.

## Presence

The first connection of a user anywhere makes them online, the last one closing (or the crash of the instance that
held it) makes them offline; see [redis.md](redis.md). Changes go to the users who share a chat with them and may see
their presence (`presence_visibility` with its exceptions). Going offline writes `users.last_seen_at`.

## Sessions

Revoking a session (logout, revoke, password change) broadcasts its id to every instance, which closes the
session's sockets with `4401`. Other sessions of the same user stay connected. Because a broadcast can be lost
(Redis unavailable) or arrive before the socket is registered, every socket also re-checks its session right after
connecting and on every second ping (about once a minute; the session cache answers most checks).

## Shutdown

On SIGTERM every socket is closed with `1012` "reconnect". The gateway waits until every connection handler has
released its presence and recorded last-seen (within `HTTP_SHUTDOWN_TIMEOUT`), then the bus, presence loop and the
ID node lease stop.

## Settings

| Variable | Default | Meaning |
|---|---|---|
| `INSTANCE_ID` | host name + random suffix | identifies the instance for presence and the ID lease |
| `NODE_ID` | leased from Redis | Snowflake node number (0-1023); set only when every instance gets its own |
| `PRESENCE_TTL` | `60s` | how long a user stays online without a heartbeat |
| `MEMBERS_CACHE_TTL` | `10m` | lifetime of the members cache |
| `MIN_CLIENT_API_VERSION` | `2.0.0-alpha.1` | oldest contract version still served |
| `WS_SEND_BUFFER` | `256` | events that may wait for a slow client |
| `WS_PING_INTERVAL` | `25s` | ping interval of idle connections |

## Not here yet

`chat_created`, `chat_list_update`, `group_created`, `group_updated` and `approval_request_created` are sent by the
chat, group and request endpoints (#53). Load tests with 10 000 idle connections per instance are part of #55.
