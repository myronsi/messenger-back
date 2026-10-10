# Message store (ScyllaDB)

Messages, reactions and the messages a user does not see live in ScyllaDB; chats, members, read positions and
attachment metadata stay in PostgreSQL ([postgres-schema.md](postgres-schema.md)). The driver is
`scylladb/gocql` (the shard-aware fork); feature code depends on `scylla.MessageRepository`, so the store can be
swapped for Apache Cassandra (same CQL) or a fake in tests.

## IDs

Message IDs are 64-bit **Snowflake IDs** (`internal/ids`): 41 bits of milliseconds since 2024-01-01, 10 bits of
node, 12 bits of sequence. They sort by time and are unique across instances as long as every running instance
has its own node number. The building blocks are here; the API takes its number from `NODE_ID` or leases a free one
from Redis once the gateway sends messages (#50): `redis.AcquireNode` writes `ids:node:{n}` with a random token of
that acquisition, `Keep` renews it every 20 s and reports a lost lease, after which the process must stop creating
IDs. Generators issue no IDs for times before 2025-01-01, so
every Snowflake ID is at least `ids.MinSnowflake` (about 1.3e17); v1 message IDs are serial numbers far below
that, so they are kept as they are, always sort before the new ones, and `ids.IsSnowflake` tells the two apart. IDs travel as strings in JSON (they exceed 2^53).

## Tables

`migrations/scylla/000001_messages.up.cql`:

| Table | Key | Purpose |
|---|---|---|
| `messages` | `((chat_id, bucket), message_id DESC)` | the messages; `bucket` is the 10-day period of the message time, so a partition never grows without bound |
| `chat_buckets` | `(chat_id, bucket DESC)` | which buckets of a chat have messages, to page across empty periods |
| `message_locations` | `message_id` | chat and bucket of a message, to find it by id alone (v1 ids carry no time) |
| `message_reactions` | `((chat_id, message_id), reaction, user_id)` | one row per user and reaction, so concurrent reactions never overwrite each other |
| `hidden_messages` | `((user_id, chat_id), message_id DESC)` | `deleted_for_me` or `not_delivered`; replaces the JSON arrays of v1 |
| `hidden_message_users` | `(chat_id, user_id)` | who hid messages in a chat, so deleting the chat finds their `hidden_messages` partitions |

The bucket of a Snowflake ID is computed from the ID; a v1 message's bucket comes from its creation time and is
looked up in `message_locations`.

## Operations

- **Insert** writes the bucket and the location (in parallel) before the message, so
  every readable message is reachable; all writes are idempotent, so a failed insert is retried with the same
  ID. They carry the message's creation time as write timestamp (`USING TIMESTAMP`), so a delayed retry loses
  against every later edit, delete or chat deletion instead of bringing the message back. (This assumes the
  API instances' clocks are not ahead of the database nodes by more than the time between a send and the
  change; keep clocks synchronized.)
- **History** (`Page`): the newest page starts at the bucket of now plus one minute (IDs of other nodes or of a
  generator borrowing future milliseconds can be slightly ahead), so for an active chat 50 messages are one
  partition read. Older pages continue in the bucket of the `before` cursor and then in older buckets from
  `chat_buckets`; `after` walks the other way, `around` centres the window on the message and gives the room one side cannot use to the other. Messages hidden for
  the viewer are left out with one range query per partition read, and the page is filled up from further rows.
- **Edit and delete** are lightweight transactions (`IF deleted = false`, `IF EXISTS`), so they are serialized per
  message and an edit racing a delete for everyone can never bring the text back. A deleted message keeps its
  ID and place and shows as deleted; its content and attachment are cleared.
- **Delete for me** inserts into `hidden_messages`; `Unhide` only clears `not_delivered`. Every write of a hidden
  row is a lightweight transaction, so `deleted_for_me` is final: a late `not_delivered` cannot replace it.
- **Reactions** of a page are read with one query (`message_id IN (...)`).
- **Unread counts** live in Redis ([redis.md](redis.md)); when they are missing, `CountAfter` recounts the messages
  after the read position that the user did not send, did not hide and that are not deleted (capped).
- **Read state** is `participants.last_read_message_id` in PostgreSQL. "Read by" for a message is the members
  whose position is at least the message ID (respecting the read-receipt privacy setting).
- **Sender profiles** of a page come from PostgreSQL in one query (`UserRepository.GetMany`).
- **DeleteChat** first removes the hidden markers, then works bucket by bucket: the reactions and locations of
  its messages (32 statements in parallel), the partition, and finally the bucket's row in `chat_buckets` as a
  checkpoint. Each bucket has its own timeout, and calling it again after a failure continues with the buckets
  that are left. It is meant for the background worker.
- **Locate** only reports messages whose row exists (an interrupted insert can leave a location behind).

## Consistency

Reads and writes use `SCYLLA_CONSISTENCY` (`local_quorum` by default), lightweight transactions `LOCAL_SERIAL`.
Every repository call runs under `SCYLLA_TIMEOUT` (5 s; a page that reads several partitions counts as one call).

## Keyspace and migrations

The keyspace is created outside the migrations because its replication differs per environment:

```sql
-- development (compose.dev.yaml creates it)
CREATE KEYSPACE messenger WITH replication = {'class': 'NetworkTopologyStrategy', 'datacenter1': 1};
-- production: three nodes per datacenter
CREATE KEYSPACE messenger WITH replication = {'class': 'NetworkTopologyStrategy', '<dc>': 3};
```

`make migrate-scylla` applies the migrations with golang-migrate (`x-multi-statement=true`: statements are split
on semicolons, so comments in the files must not contain one); `make migrate-scylla-down` rolls them back.

## Tests

The tests skip unless `TEST_SCYLLA_HOSTS` is set. They create a keyspace of their own, apply the migrations and
drop it afterwards. A local node for them:

```sh
docker run -d --name scylla -p 127.0.0.1:9042:9042 -p 127.0.0.1:19042:19042 scylladb/scylla:2026.1 \
  --smp 1 --memory 1G --overprovisioned 1 --developer-mode 1 --broadcast-rpc-address 127.0.0.1
TEST_SCYLLA_HOSTS=127.0.0.1:9042 go test ./internal/store/scylla/
```

`--broadcast-rpc-address 127.0.0.1` matters: the driver connects to the address the node advertises, which is
otherwise the container's internal one.
