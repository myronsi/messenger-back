# WebSocket protocol (API contract v2)

The JSON Schemas in `websocket/client/` (client → server) and `websocket/server/` (server → client) are the source of truth. Every event has an example in `websocket/examples/`, validated against its schema by `npm run check`. TypeScript types are generated into `dist/ws-events.d.ts` (`ClientEvent`, `ServerEvent`).

## Connecting

1. `POST /api/v2/ws/ticket` (authenticated) returns `{ "ticket": "…", "expires_in": 30 }`. The ticket is single-use and short-lived, so no access token ends up in a URL or a log.
2. Open **one connection per browser tab**: `wss://<host>/api/v2/ws?ticket=<ticket>&api_version=<contract version>`. The connection carries the events of all chats of the user. `api_version` is optional and takes the place of the `X-Client-Api-Version` header, which browsers cannot send on a WebSocket.
3. The first server event is `hello` with `api_version` and `min_client_api_version`. A client that reconnects after a deploy compares them with the contract version it was built for and shows an upgrade notice instead of failing at random.
4. When the ticket is invalid or expired the server closes the connection with code `4401`. A client whose `api_version` is no longer served is closed with `4426` after `hello`.

## Close codes

| Code | Meaning | Client |
| --- | --- | --- |
| `1012` | `reconnect`: the server is restarting, or it lost its pub/sub connection and events may be missing | reconnect with backoff, then load what was missed (`after` cursor) |
| `1013` | the client did not keep up with its events | reconnect, then catch up |
| `4401` | invalid ticket, or the session ended (logout, revoked, password changed) | get a new ticket; if that fails, sign in again |
| `4426` | the client's contract version is no longer served | show the upgrade notice |

## Envelope

Server → client:

```json
{ "type": "message", "event_id": "7217400317439950849", "chat_id": "42", "data": { "message": { "…": "…" } } }
```

Client → server:

```json
{ "type": "message", "client_temp_id": "c-1", "chat_id": "42", "data": { "type": "text", "content": "Hello!" } }
```

- All IDs are strings (Snowflake IDs do not fit into a JavaScript number).
- `event_id` is unique and increases per server; it is only for deduplication and logs.
- `chat_id` is always `null` in `hello`, `presence` and `approval_request_created`, which do not belong to a chat. `ack` and `error` repeat the `chat_id` of the client event they answer, and it is `null` for connection-level errors.
- Unknown event `type`s must be ignored by clients, so new event types are not a breaking change.
- A user who is removed from a chat (or whose chat is deleted) receives `chat_deleted` and no further events of that chat.

## Acknowledgements

Every client event except `typing` (which is not acknowledged and carries no `client_temp_id`) has a `client_temp_id` (1–64 characters, unique per client). The server answers with exactly one of:

- `ack` — `client_temp_id`, and for `message` the stored `message_id` and `created_at`. Sending the same `client_temp_id` again returns the same `ack` (idempotent), which makes retries after a reconnect safe.
- `error` — `client_temp_id` and `data.code`/`data.message`. `code` is one of the stable codes of `ErrorCode` in `openapi.yaml` (the same codes as the REST `application/problem+json` errors).

The sender also receives the broadcast `message` event with the stored message (its `client_temp_id` is included, so the client can replace its pending copy).

## Client → server events

| Type | Purpose |
| --- | --- |
| `message` | Send a message (`type` `text`, `file` or `voice`; files are referenced by `attachment_id`, never by URL) |
| `resend` | Deliver a stored but undelivered message again |
| `edit` | Edit your own text message |
| `delete` | Delete a message for yourself (`scope: me`) or everyone |
| `read` | Mark messages as read up to `message_id` |
| `reaction_add`, `reaction_remove` | Add or remove a reaction |
| `typing` | Typing indicator (not acknowledged) |

## Server → client events

| Type | Meaning |
| --- | --- |
| `hello` | First event: `api_version`, `min_client_api_version`, `user_id` |
| `ack`, `error` | Result of a client event, references `client_temp_id` |
| `message`, `edit`, `delete` | Message created, edited or deleted |
| `reaction_add`, `reaction_remove` | Reactions changed |
| `read` | A user read up to `message_id` |
| `typing` | A user is typing |
| `chat_created`, `chat_deleted`, `chat_list_update` | Chat list changes |
| `group_created`, `group_updated` | Group changes (members, roles, name, avatar) |
| `approval_request_created` | New request in the inbox |
| `presence` | A user came online or went offline |

## Compatibility

Adding an event type or an optional field is a MINOR change; removing or renaming an event or field, or making a field required, is MAJOR (see `docs/api-compatibility.md`). Clients must ignore unknown events and unknown fields.
