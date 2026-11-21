# Pinned messages

Contract 2.1.0-alpha.42. Who can pin a message, who sees the pin, and what removes it.

## Two kinds of pins

| Pin | Who sees it | Limit |
|---|---|---|
| For everyone (`scope: everyone`) | Every member of the chat, unless they hid it for themselves | 500 per chat |
| Only for me (`scope: me`) | Only the person who pinned it | 500 per person and chat |

A message pinned both ways shows once, as a pin for everyone. When the pin for everyone goes away, the person's own pin shows again.

## Who may do what

| Chat | Pin for me | Pin for everyone | Unpin for everyone |
|---|---|---|---|
| Direct chat | Both people | Both people | Both people |
| Saved Messages, a chat still in Requests, a direct chat with a block | Yes | No (`403`) | Yes |
| Group, member | Yes | No (`403`) | No |
| Group, moderator, admin, owner | Yes | Yes | Only when their role is at least the pinner's current role (owner > admin > moderator). A pinner who left or is now a member counts as member. |

Anyone can hide a pin for everyone only for themselves (`DELETE …/pin?scope=me`); pinning the message for themselves shows it again. System and call messages cannot be pinned (`422`).

## What people see

- `GET /chats/{chat_id}/pins` lists the pins the caller sees, newest pin first, with `can_unpin_for_everyone` per pin. `Chat.pinned_message_count` counts the same pins.
- Pinning or unpinning for everyone posts a system message `message_pinned` or `message_unpinned` with `message_id`, a 60-character `snippet` and `message_type`. System messages never send a push notification and do not count as unread.
- `pin_update` goes to every member for a change for everyone, and only to the person's own sessions for their own pins and hides (with their new `pinned_message_count`).

## What removes pins

| Event | Result |
|---|---|
| Message deleted for everyone | All its pins are removed |
| Message deleted for me | My own pin is removed and the pin for everyone is hidden for me |
| A pin for everyone of a message I deleted for myself earlier | Hidden for me from the start; I get no `pin_update` |
| History cleared for me | Pins of the cleared messages are no longer listed for me |
| History cleared for everyone | Pins of the cleared messages are removed |
| Leaving a group or being removed | My own pins and my hides in that group are removed; pins for everyone stay |
| Group deleted | Everything is removed with the chat |

## Storage

PostgreSQL, migration `000054_message_pins`: `message_pins` (for everyone, one row per message), `message_pins_personal` (own pins) and `message_pin_hides` (pins for everyone hidden by one person; removed together with the pin). The message itself stays in ScyllaDB. A pin for everyone locks the chat row and an own pin locks the user row, so the limits hold under parallel requests.
