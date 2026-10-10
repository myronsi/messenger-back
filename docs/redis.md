# Redis

Redis (or Valkey) holds what has to be shared between API instances or is too hot for the databases. The
client is `redis/go-redis/v9`; the components live in `internal/store/redis` (authentication keeps its keys in
`internal/auth`, see [auth.md](auth.md)).

**Redis is not a source of truth.** Everything except rate limits and one-time credentials (WebSocket tickets,
2FA login challenges) can be rebuilt from PostgreSQL and ScyllaDB. Losing Redis logs everybody's sockets out
of presence for a minute and makes the first requests slower; it loses no data.

## Keys and channels

Production keys have no prefix; tests put a random namespace in front of every key.

| Purpose | Key / channel | Type | Expiry | Code |
|---|---|---|---|---|
| Presence | `presence:{user_id}`: field `i:{instance}` → claim expiry (ms), field `v` → version | hash | claims 60 s (the TTL), refreshed by heartbeats; the hash 2 × that | `Presence` |
| Presence index | `presence:index`: user id scored by its latest claim expiry | sorted set | entries removed by the sweep | `Presence` |
| Last seen | written to PostgreSQL (`users.last_seen_at`) when the user goes offline | – | – | gateway |
| Live delivery | `user:{user_id}` | pub/sub channel | – | `Bus` |
| Typing indicator | published on `user:{user_id}`, never stored | pub/sub | – | `Bus` |
| Unread counters | `unread:{user_id}`: field per chat id, plus `_built` | hash | none (rebuilt when `_built` is missing) | `Unread` |
| Chat membership cache | `members:{chat_id}` (user ids plus the marker `0`), `members:{chat_id}:v` (version) | set, string | 10 min; version 24 h | `Members` |
| Rate limits (frequent actions) | `rl:{action}:{digest of ip or user}` | string (GCRA) | until the bucket is full again | `RateLimiter` |
| Rate limits (auth failures) | `auth:rl:{rule}:{digest}` | sorted set (sliding window) | the window | `auth.Limiter` |
| WebSocket ticket | `auth:wsticket:{digest}` → user and session id | string | 30 s, single use (`GETDEL`) | `auth.Tickets` |
| 2FA login challenge | `auth:2fa:{digest}` | hash | 5 min, 5 attempts | `auth.Challenges` |
| Session cache | `auth:sess:{session_id}` | string | `SESSION_CACHE_TTL` (30 s) | `auth.SessionCache` |
| Durable events | `events:messages`, `events:users` | stream | trimmed with `MAXLEN ~` | #49 |

Personal data (IP addresses, usernames) only reaches Redis as a digest.

## Presence

Each instance counts the connections of its users itself. The first connection of a user on an instance
*claims* the user in `presence:{user_id}`; the last one to close releases the claim. A user is online while any
instance holds a claim that has not expired, so the answer is the same on every instance.

- **Heartbeats** renew the claims of all local users every TTL / 3. If Redis lost a claim (restart,
  flush) the heartbeat writes it again and reports the user online.
- **Crashed instances** stop heartbeating. Their claims expire, and the sweep that every instance runs every
  TTL / 4 finds those users through `presence:index` and reports them offline. The hash outlives its
  claims (2 × TTL) for exactly this reason. Sweeps of several instances are safe: each transition is reported
  by one of them.
- Every transition carries a **version** that increases per user. Presence events of different instances can
  overtake each other on the way to a client, so the gateway forwards only changes newer than the last one it
  sent for that user.
- Times are read from the Redis clock, so instances with skewed clocks agree on what has expired.

## Live delivery (pub/sub)

The gateway subscribes to `user:{user_id}` while the user has a connection on the instance (one subscription
per user, however many tabs). Whoever produces an event publishes it to the channel of every recipient: for a
new message, every member of the chat. For very large groups (thousands of members) this becomes many
publishes per message; per-chat channels are the planned next step then.

Pub/sub does not store anything. An event published while an instance is reconnecting to Redis is lost for its
users; clients load what they missed through REST after every reconnect (the `after` cursor). Work that must
not be lost (search indexing, push) goes through the event streams.

## Unread counters

`unread:{user_id}` has one field per chat with unread messages. A new message increments the field of every
member except the sender, reading resets it (or sets it to the number of messages after the read position).
The hash carries `_built` once it holds the counts of all chats; when it is missing (a new Redis, an evicted
key), the counts are rebuilt from ScyllaDB and the read positions in PostgreSQL. Increments for a user whose
hash is not built are skipped, because the rebuild counts those messages anyway. A message that arrives during
a rebuild can be missed until the user next reads that chat.

## Membership cache

`members:{chat_id}` caches the members of a chat for the gateway's authorization checks and the fan-out.
Every committed membership change (member added, removed, left, chat or account deleted) must call
`Members.Invalidate`. A cache fill that read the members before such a change cannot write them afterwards:
the invalidation increments the version, and a fill only writes when the version is still the one it saw
before loading.

## Rate limits

Frequent actions (sending messages, typing, reactions) use GCRA: one key per subject holding a single
timestamp, `Rate` events per `Period` with bursts of up to `Burst`. The authentication limits count failures
in a sliding window and are in the auth package. Both fail closed: when Redis is unavailable the action is
refused.

## Timeouts and errors

Every command runs with a deadline of `REDIS_TIMEOUT` (2 s), or the caller's shorter one; a pipeline counts as
one command. Blocking reads (`XREADGROUP … BLOCK`, `BLPOP`, …) are exempt and use the caller's context. Failed
commands are counted in `messenger_store_errors_total{store="redis"}` (a missing key is not a failure).

## Operating Redis

- **Version:** Redis 7.2+ or Valkey 8 (the scripts use `ZRANGE … BYSCORE` and `ZADD GT`).
- **Topology:** a primary with replicas and Sentinel, or a managed service with failover. Redis Cluster is not
  supported: some scripts touch two keys of one chat that are not in the same hash slot.
- **Persistence:** AOF on (`appendonly yes`, `appendfsync everysec`), so counters and rate limits survive a
  restart. The development stack runs with AOF.
- **Memory:** set `maxmemory` with headroom and keep the default policy **`noeviction`**. Evicting keys with an
  expiry (`volatile-*`) would drop rate limits and tickets first, and `allkeys-*` would also drop unread
  counters. With `noeviction` a full Redis rejects writes, which shows up as errors in the metric above;
  alert on memory use at 80 % instead. Rough sizing: about 200 bytes per online user (presence, subscription),
  about 50 bytes per chat with unread messages per user, and about 100 bytes per member of every chat that was
  active in the last 10 minutes.
