# Message search (Elasticsearch)

`internal/search` keeps an Elasticsearch index of messages and answers searches with it. Elasticsearch never decides who sees what. Every query is limited to the caller's chats and leaves out what they deleted for themselves, and every hit is read from ScyllaDB again before it is shown, so an index that lags behind cannot show a deleted message.

## Endpoints

- `GET /search/messages?q=` searches every chat of the caller. Filters: `chat_id`, `sender_id`, `type` (`text`, `file`, `voice`), `from`, `to`.
- `GET /chats/{id}/messages/search?q=` searches one chat (`404` for a chat the caller is not in).

Both answer a `MessageSearchPage`:
- **Order and paging:** newest first, paged with `search_after` on (creation time, message id). The cursor is opaque; one that is not ours is `400`.
- **Excerpt:** `highlight` is a plain-text excerpt in which the matched words are wrapped in U+E000 and U+E001 (private-use characters, never HTML).
- **Limits:** queries are 2 to 128 characters, pages at most 50 hits. Searches are rate limited per user (60 per minute, bursts of 20). Elasticsearch being away is `503`.

## Index

- **Alias and indices:** writes and searches go through the alias `messages` (`SEARCH_ALIAS`). The indices behind it are `messages-v<n>`, one at a time.
- **Setup:** the worker installs the index template on start and creates `messages-v1` behind the alias when there is none. While Elasticsearch is away it retries, and the other consumers keep running.
- **Fields:**

| Field | Type |
|---|---|
| `message_id` | `keyword` (also the document id) |
| `chat_id`, `sender_id`, `type` | `keyword` |
| `content` | `text` (folded: lower case, accents removed), with `content.en` and `content.ru` (English and Russian stemming) |
| `file_name` | `text` (folded) and `file_name.raw` (`keyword`) |
| `hidden_for` | `keyword` array: users who deleted the message for themselves |
| `created_at` | `date` |

The mapping is strict, with 1 shard and `SEARCH_REPLICAS` replicas (1 by default; the dev stack, a single node, uses 0).

## Keeping it current

The worker's consumer group `search-indexer` reads `events:messages` and `events:chats` ([events.md](events.md)):

- **`message.created`, `message.edited`:** the message is read from ScyllaDB and written (an upsert that keeps `hidden_for`). A message that is gone or deleted is removed instead.
- **`message.deleted`:** the document is removed.
- **`message.hidden`** (deleted for one user): the user is added to `hidden_for`. If the document is not there yet, a stub holding only `hidden_for` is written, which the created event then fills.
- **`chat.deleted`:** the chat's documents are removed (`_delete_by_query`).

Changes are searchable within about a second (the index refresh interval). Messages hidden as "not delivered" are not in `hidden_for`; the ScyllaDB check at query time leaves them out.

**Reconciliation:** every `SEARCH_RECONCILE_EVERY` (1 h) the worker re-indexes the messages of the last `SEARCH_RECONCILE_WINDOW` (2 h) of every chat with activity in that window. This repairs what the stream missed (Redis away for a moment, entries trimmed before they were indexed).

## Rebuilding

`worker reindex` (the same image: `/app/worker reindex`) rebuilds the index from ScyllaDB without downtime:

1. It creates `messages-v<n+1>` and writes every chat's messages into it, with `hidden_for` from the deleted-for-me markers.
2. It writes the messages of its own run time again, then switches the alias in one step and deletes the old index.

Searches use the old index until the switch. Run it after a mapping change, or after the migration from v1 (#54).

## Security

Elasticsearch is reachable only from the API and the worker, never from clients. In production:
- use TLS (`https://` in `ELASTICSEARCH_URL`);
- connect as a dedicated user whose role grants `messages*` indices and the `messages` template only.

The dev stack and CI run without security.

## Deviation from #52

The code talks to Elasticsearch over plain HTTP with JSON (`internal/store/elastic`), as the readiness probe already did, rather than with `go-elasticsearch`. The handful of APIs it uses (index template, alias, update, bulk, search) do not justify the dependency.

User search stays in PostgreSQL (`pg_trgm`, [accounts.md](accounts.md)), which #52 allows.
