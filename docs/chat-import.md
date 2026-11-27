# Chat import

Contract 2.1.0-alpha.46. How a history from Telegram or WhatsApp becomes part of a chat, who may import, and how people confirm which imported messages are theirs.

## Flow

1. The client reads the export on the device (Telegram Desktop JSON, WhatsApp chat export) and shows a preview. The server never reads export files.
2. `POST /chats/{chat_id}/imports` starts the import with the number of messages, the period and every sender name. One name can be `me`; other names can be suggested for members of the chat (`user_id`).
3. The client uploads the files with `POST /attachments` (with `?import_id=`: its own rate limit, checked before the body is read, of 1,200 per minute, and JPEG, PNG and still WebP images are stored with their metadata removed while the thumbnail, display copy and placeholder follow in the `import_image` media job, claimed after live jobs and one at a time; imported stickers that are already 512×512 WebP files within the sticker limits are stored the same way, without metadata, and their panel and message variants follow in the `import_sticker` job, which shares that one import slot), then sends the messages in order with `PUT /imports/{import_id}/messages` (at most 500 per batch, `from_index` = `next_index`). A batch that was stored already answers with the current `next_index`, so retries are safe and an interrupted import continues with `GET /imports/{import_id}`.
4. `POST /imports/{import_id}/finish` adds the closing line, marks members' read positions past the block, tells every member to reload the chat, and asks each suggested member to confirm.

## Who may do what

| Action | Who |
|---|---|
| Import into a direct chat | Either person, not in a chat still in Requests or with a block |
| Import into a group | Admins and the owner |
| Add messages, finish, suggest members for names | The importer |
| Confirm, decline, withdraw a name | Only the member suggested for that name, after the import is finished |
| Remove an import | The importer, and the owner of a group |
| Delete an imported message for everyone | The importer, the confirmed member for their own messages, and group staff as for other messages |
| Edit an imported message | Nobody |

## Senders

| State | Shown as |
|---|---|
| `me` | The importer's account |
| `pending` | Text name; only the importer and the suggested member see the suggestion |
| `confirmed` | The member's account |
| `declined`, `unassigned` | Text name |

Messages are stored under the importer's account. The shown sender is worked out when messages are read (`messages.ImportResolver` wraps the message repository), so confirming or withdrawing a name never rewrites messages. A member who declines or withdraws a name cannot be suggested for it again. A deleted account turns its confirmed names back into text.

Reports of an imported message with an unconfirmed name name the importer as the sender.

## Where the messages go

Message ids are Snowflakes that hold their time, and history is sorted by id, so imported messages cannot use their real date. The server places them in the gap between the lower bound of the chat (2025-01-01, a cleared history, or an earlier import whose period comes first) and the next message above it, in their original order. `Message.created_at` is the original time; `Message.import` says where the message came from.

- Node numbers 1008–1023 are reserved for imports; running instances lease only 0–1007 (`ids.MaxLiveNode`).
- The import is cut into chunks of 5,000 positions. Each chunk gets a short window of milliseconds on one import node, spread over the lower half of the gap, so a later import whose period follows still finds room. `chat_import_windows` keeps the windows; an exclusion constraint stops two imports from sharing one.
- The id of position `k` (0 is the opening line, the messages follow, the last is the closing line) is computed from its window, so a retried batch writes the same ids. Imported rows carry the import's start as their write timestamp.
- An import whose period overlaps another import of the chat is refused (`409`), as is a second running import of the chat, a second running import of the same user in any chat, and a gap too small for the messages.
- `chat_imports.last_activity_at` moves with every accepted batch. The API instances check every 10 minutes (`RunImportExpiry`) and remove running imports without a batch for 24 hours (`ImportIdleTimeout`), the same way as `DELETE`: the chat is free again and the partial history disappears. Clients upload the files of each batch right before sending it, so a long upload does not count as idle.

## Visibility while running

Until `finish`, only the importer sees the import's messages. For every other member, history pages, the chat list preview, unread counts and the first unread message, the media list and the link list leave out the messages of a running import: a page that reaches its id range continues on the other side of it, `around` one of its messages answers `404`, and the media and link cursors jump past it. Other members see the `import_update` progress instead, and `history_imported` on finish makes them reload the chat with the whole import at once. Live messages are never affected: they use other id nodes, so chatting goes on during an import.

## Unread

Imported messages never count as unread: unread ids and counts skip them, and `finish` moves every read position that is unset or inside the import's range to its last id. Imports sit below every visible live message, so live unread messages stay unread. Migration 000063 applied the same to imports that finished before.

## Removing

`DELETE /imports/{import_id}` marks the import `removing`: reads drop its messages at once. The worker (`import-cleanup` consumer of `events:chats`) deletes the messages, reactions, locations and hidden markers of its id range in ScyllaDB, the attachment links, link entries and pins in PostgreSQL, and then the import rows. The search indexer deletes the documents of the range.

## Events and sync

| What | Who gets it |
|---|---|
| `import_update` (`running`, `done`, `removing`) | Every member |
| `import_sender_update` for a suggestion or a decline | The importer and the suggested member |
| `import_sender_update` for a confirmation or a withdrawal | Every member |
| Sync change `history_imported` | Every member, on finish, removal, confirmation and withdrawal |
| `chat.history_imported` (events:chats) | Search indexer: index the id range again |
| `chat.import_removed` (events:chats) | Search indexer and import cleanup |

Imported messages send no push notification and do not count as unread.

## Limits

500,000 messages, 5,000 files and 200 sender names per import; 500 messages per batch. Files follow the normal upload limits.

## Storage

PostgreSQL, migration `000057_chat_imports`: `chat_imports`, `chat_import_senders`, `chat_import_windows`, and the sync change kind `history_imported`. ScyllaDB, migration `000006_message_imports`: `messages.import_id`, `import_sender` and `original_at`.
