# User reports and automatic account status

Users can report other users. Reports drive an account status that restricts the reported account step by step. Everything is automatic, including the strictest level and appeals; there is no staff role and no human review. Code: `internal/moderation`, store `internal/store/postgres/moderation.go`, migration `000044_user_moderation`.

## Reporting

`POST /users/{user_id}/report` `{reason, note?, block}` → `204`; `DELETE /users/{user_id}/report` → `204`. Withdrawing marks the report (`withdrawn_at`) instead of deleting it: it stops counting, but it still counts toward the reporter's daily limit, and reporting the same user again revives it with its first weight and `created_at`.

- Reasons: `spam`, `scam`, `harassment`, `impersonation`, `inappropriate_profile`, `other`. The note is optional, at most 500 characters, and never shown to the reported user.
- One active report per reporter and target (`user_reports` primary key). Reporting again changes only the reason, the note and `updated_at`; the weight and `created_at` stay, so re-reporting cannot refresh or raise a report.
- At most 20 reports per reporter in 24 hours (`429`); updates and withdrawals count too. Reporting yourself is `422`, an unknown user `404`.
- `block` (default `true`) blocks the target like `PUT /me/blocked-users/{id}`, including the hook that ends calls between the two.
- The target never learns who reported them. The report stores a snapshot of the target's display name, username, avatar attachment id and bio, and the context of the pair. No message content is stored.
- Every report and withdrawal emits `user.reported` (`user_id` = target, `actor_id` = reporter) on `events:users`.

## Weight of one report

Computed when the report is first made (`moderation.Weight`) and stored in `user_reports.weight`.

| Context (`user_reports.context`) | How it is found | Weight |
|---|---|---|
| `target_initiated` | a chat the target created that both are in (their direct chat or a group the target created), an approval request (direct message or group invitation) from the target to the reporter, or a call the target started in a chat the reporter is in | 1.0 |
| `shared_chat` | any other chat both are in | 0.5 |
| `none` | no chat in common (found by search or a profile link) | 0.25 |

Then, in this order:

- Reporter account younger than 24 hours, or reporter status `restricted` or `suspended`: 0.
- Burst: a `none` report while more than 5 `none` reports on the same target arrived within the last hour (this one included): 0, and the other `none` reports of that hour are set to 0 as well.
- Reporter reported more than 10 other users in the last 30 days: half (rounded to two decimals, so 0.25 becomes 0.13).

A group member added by an admin who did not create the group counts as `shared_chat`: member additions are only recorded as system messages in ScyllaDB.

## Score and statuses

`score = Σ weight × decay`, where a report younger than 30 days counts fully, one of 30 to 90 days counts half, older ones not at all. Reports older than 180 days are deleted by the daily job.

| Status | Enters at | Restricted (`AccountRestriction`) |
|---|---|---|
| `good` | score < 3 | nothing |
| `warned` | 3 | nothing; clients show a banner |
| `limited` | 6 | `start_direct`, `invite_to_group`, `call` with non-contacts; at most 5 new direct chats in 24 hours; new chats land in the recipient's Requests folder |
| `restricted` | 10 | the above, plus `create_group`, `upload`, `forward` (files), `edit_profile` (display name, username, avatar, music, background), `publish_pack` (create sticker or emoji packs, add stickers or emoji), `search` (hidden from `GET /users?q=`); group calls refused, calls only with contacts |
| `suspended` | 15 **and** at least 8 distinct reporters with a stored weight ≥ 0.5 **and** reports on at least 3 different days (UTC) | everything above plus `send` (send, resend, edit, react, forward; typing indicators are dropped), joining calls (`call`) and `manage_group` (rename, avatar, background, roles, ownership, removing members): read-only. Others see `account_label: restricted` on the user ("Restricted account") |

A "contact" here is someone who took part in a chat with the target: `SharedChatViewers(target, actor)`, the same rule as the privacy settings ([accounts.md](accounts.md#privacy)). Reading own chats, the account-status page, appeals, data export and account deletion never check the status.

## Status changes

`moderation.Next` decides; `Recompute` runs it in one transaction with the moderation row locked, writes the new state and, when the status changed, a `user_moderation_events` row (`from_status`, `to_status`, `score`, `cause`).

- A new weighted report (weight > 0, created after `status_since`) raises the status to the one the score calls for (`cause: reports`).
- Without a new weighted report the status only goes down: to the score's status when decay lowered the score (`decay`), and one level when the last 30 days had neither a weighted report nor a status change (`quiet`).
- Appeal (`POST /me/account-status/appeal`, once per 30 days, only when `can_appeal`): the status drops one level at once and a 14-day probation starts (`appeal`). Without a new weighted report in those 14 days the lower level stays (`probation_passed`). A new weighted report during probation restores the status (at least one level above the probation level, or what the score calls for) and blocks appeals for 90 days (`probation_failed`). Otherwise `409`.

Every change invalidates the cached status, counts `messenger_moderation_status_changes_total{status}` and sends the WebSocket event `account_status` (an `AccountStatus`) to the user's own sessions.

## Enforcement

`moderation.Service.Allow(ctx, actor, action, target)` is the single check. It returns `*moderation.ErrRestricted{Action}`, which the HTTP layer answers with `403` `account_restricted` and `restriction`, and the WebSocket with an `error` event carrying the same fields.

| Action | Checked in |
|---|---|
| `start_direct` | `chats.CreateDirect` (new chats and requests only, not reopening an existing chat) |
| `invite_to_group` | `chats.CreateGroup` (each initial member), `chats.AddMember` |
| `create_group` | `chats.CreateGroup` |
| `call` | `calls.Start` (direct: the peer; group: no target); `calls.Join` (suspended only) |
| `manage_group` | `chats.UpdateGroup`, `SetGroupAvatar`, `SetGroupBackground`, `SetRole`, `TransferOwnership`, `RemoveMember` of someone else (suspended only) |
| `send` | `messages.Send`, `Resend`, `Edit`, `React`, `ForwardMany`; `Typing` drops the indicator |
| `forward` | `messages.ForwardMany` when a forwarded message has a file (stickers excluded) |
| `upload` | `POST /attachments`, `POST /upload-sessions` (`httpapi.Server`) |
| `edit_profile` | `PATCH /me` with `display_name` or `username`, `PUT /me/avatar`, `PUT /me/music`, `PUT /me/background` |
| `publish_pack` | `POST /stickers/packs`, `POST /stickers/packs/{id}/stickers`, `POST /emoji/packs`, `POST /emoji/packs/{id}/emoji` |
| `search` | `GET /users?q=` leaves out `restricted` and `suspended` users (except the viewer) |

The status is cached in Redis (`moderation:{user_id}`, 1 minute) and read from PostgreSQL on a miss. When neither answers, `Allow` fails closed (`503` with `Retry-After`, `internal_error` on the WebSocket), like the membership cache. `account_label` comes from the same cache (`Service.Labels`, one `MGET` per rendering; misses are read from PostgreSQL in one query and cached, `good` included), and is left out when neither answers.

`chats.Service.MustBeRequest(requester, recipient)` tells whether a new direct chat must land in the recipient's Requests folder: true for `limited` and worse, and when the status cannot be read. `CreateDirect` hands such chats to `chats.Deps.Requests` (`MarkRequest`); that dependency is wired once the Requests folder (`participants.is_request`) exists.

## Worker

- Consumer group `moderation` on `events:users`: `user.reported` → `Recompute` of the target ([events.md](events.md)).
- Daily job (at start and every 24 hours, once per 24-hour slot across all worker instances through the Redis key `once:moderation_daily:…`): recomputes every account that is not `good` or has a probation running (decay, probation ends, the 30-day step down), then deletes reports older than 180 days.

## Alerts

- `messenger_moderation_status_changes_total{status}` and `messenger_user_reports_total{reason, weight_bucket}` (`zero`, `low` < 0.5, `medium` < 1, `full`).
- Alert when status changes per hour jump far above their 7-day average; this replaces a human reviewer as the signal that the thresholds are wrong or the system is being abused:

```promql
sum(increase(messenger_moderation_status_changes_total[1h]))
  > 5 * (sum(increase(messenger_moderation_status_changes_total[7d])) / 168) + 10
```

A rising share of `weight_bucket="zero"` reports points to brigading by new or restricted accounts.

## Tables

- `user_reports (reporter_id, target_id, reason, note, context, weight NUMERIC(3,2), snapshot JSONB, created_at, updated_at, withdrawn_at)`, primary key `(reporter_id, target_id)`, indexes `(target_id, created_at)` and `(reporter_id, created_at)`. Both users `ON DELETE CASCADE`.
- `user_moderation (user_id, status, score, status_since, probation_until, appeal_blocked_until, last_appeal_at)`: one row per user who was ever recomputed or appealed; no row means `good`.
- `user_moderation_events`: the audit log of status changes.
