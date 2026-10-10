# Domain events and the worker

Work that does not have to happen inside a request is driven by events: deleting a chat's messages, cleaning up
counters, and (with #52) search indexing, later push notifications. The request path stores the change, delivers it
live (pub/sub, [realtime.md](realtime.md)) and then appends an event to a Redis stream; the worker (`cmd/worker`)
consumes the streams.

## Write path of a new message

1. Validate (membership, privacy, blocks, limits) — `messages.Service`.
2. Write to ScyllaDB, the source of truth.
3. Publish to the members' `user:{id}` channels for live delivery.
4. `XADD events:messages` with `type=message.created`, `chat_id`, `message_id`, `actor_id`, `at`.
5. Answer the sender with `ack`.

A failure in step 3 or 4 does not undo the message. Clients catch up through REST; consumers rely on their
reconciliation jobs (below).

## Events

`internal/events`. Entries are flat: the type, the ids involved and the time. Consumers load whatever else they need,
which keeps entries small and handlers idempotent.

| Stream | Types | Fields |
|---|---|---|
| `events:messages` | `message.created`, `message.edited`, `message.deleted`, `message.hidden` (deleted for one user), `reaction.changed` | `chat_id`, `message_id`, `actor_id`; `user_id` for `message.hidden` |
| `events:chats` | `chat.member_added`, `chat.member_removed`, `chat.deleted` | `chat_id`, `user_id` (the member), `actor_id` |
| `events:users` | `user.updated`, `user.deleted` | `user_id` |

Every stream is trimmed to about a million entries (`MAXLEN ~`).

The message service emits the message events today; the chat, group and account endpoints (#53) emit the chat and
user events.

## Consumers

`redis.Consumer` reads one stream as a member of a consumer group (one group per purpose; every group sees every
entry; the instances of the worker share the group's entries):

- **At least once.** An entry is acknowledged (`XACK`) only after its handler succeeded. Handlers are idempotent
  (keyed by message or chat id).
- **Retries with backoff.** A failed entry stays pending and is tried again after `RetryAfter` (10 s), then
  2 × that, 3 × that, … The schedule is kept in `<stream>:retry:<group>` (entry id → "not before, failures").
  A handler that panics fails like one that returns an error; the worker keeps running.
- **Dead letters.** After 5 failures the entry is copied to `<stream>:dead` with `dead_group`, `dead_entry` and
  `dead_error`, and acknowledged. A reclaimed entry is counted as failed *before* its handler runs, so an entry that
  takes the whole process down every time (out of memory, killed) is dead-lettered too, instead of crash-looping.
  `messenger_events_handled_total{outcome="dead_letter"}` counts them, as well as pending entries that were trimmed
  from the stream before anyone handled them (logged as an error); alert on it.
- **Crashed consumers.** Entries a consumer took but never acknowledged are reclaimed with `XAUTOCLAIM` by any
  consumer of the group once they are idle for `RetryAfter`. While a consumer works through a batch it touches the
  entries it has not finished every `RetryAfter / 3` (`XCLAIM … JUSTID`, only for entries it still owns), so slow
  handlers are not mistaken for crashed ones and run twice in parallel. Retry records are written only while the
  consumer still owns the entry.
- **Later, not failed.** A handler can return `redis.RetryLater{After}` to get the entry back after a delay without
  it counting as a failure (the second pass of a chat deletion uses this).
- **New groups** start at the beginning of what the stream still holds, so adding a consumer replays the retained
  history (handlers are idempotent).

Stopping the worker loses nothing: on restart each group continues after its last delivered entry, and entries that
were in flight are reclaimed.

| Group | Stream | Does |
|---|---|---|
| `chat-cleanup` | `events:chats` | `chat.deleted`: deletes the chat's messages from ScyllaDB, then once more after the grace period (`scylla.DeleteGracePeriod`) for writes that were in flight; `chat.member_removed`: drops the chat from the member's unread counters unless they are a member again by then (entries can be handled late or replayed) |
| `search-indexer` | `events:messages` | #52 |

## Reconciliation

Step 4 can fail after step 2 succeeded (Redis unavailable for a moment), and a consumer that falls behind by more than
the trimmed length misses entries. Each consumer that keeps derived data therefore has a reconciliation job that
compares with the source of truth periodically; for the search index that is part of #52 (re-indexing recent
buckets). Chat deletion needs none: deleting the chat in PostgreSQL removes every way to reach its messages, and the
event only cleans up storage.

## Metrics

- `messenger_events_handled_total{group, outcome}`: `ok`, `failed` (will be retried), `dead_letter`.
- `messenger_store_errors_total{store="redis"}` covers failed stream reads and writes.
