# User reports and automatic account status

Users can report other users. Reports drive an account status that restricts the reported account step by step. The levels and appeals are automatic; the moderation service (repository `messenger-moderation`) lets a person on the moderation team set a status with a reason, dismiss reports and hide packs ([Staff decisions](#staff-decisions)). Code: `internal/moderation`, store `internal/store/postgres/moderation.go`, migrations `000044_user_moderation` and `000046_moderation_decisions`.

## Reporting

`POST /users/{user_id}/report` `{reason, note?, block}` → `204`; `DELETE /users/{user_id}/report` → `204`. Withdrawing marks the report (`withdrawn_at`) instead of deleting it: it stops counting, but it still counts toward the reporter's daily limit, and reporting the same user again revives it with its first weight and `created_at`.

- Reasons: `spam`, `scam`, `harassment`, `impersonation`, `inappropriate_profile`, `other`. The note is optional, at most 500 characters, and never shown to the reported user.
- One active report per reporter and target (`user_reports` primary key). Reporting again changes only the reason, the note and `updated_at`; the weight and `created_at` stay, so re-reporting cannot refresh or raise a report.
- At most 20 reports per reporter in 24 hours (`429`); updates and withdrawals count too. Reporting yourself is `422`, an unknown user `404`.
- `block` (default `true`) blocks the target like `PUT /me/blocked-users/{id}`, including the hook that ends calls between the two.
- The target never learns who reported them. The report stores a snapshot of the target's display name, username, avatar attachment id and bio, and the context of the pair. No message content is stored.
- Every report and withdrawal emits `user.reported` (`user_id` = target, `actor_id` = reporter) on `events:users`.

## Content reports

Members can report a message or a group. Code: `internal/contentreports`, store `internal/store/postgres/content_reports.go`, migration `000048_content_reports`. The moderation service (`messenger-moderation`) reads the table read-only; moderators see only the copy, never the chat.

- `POST /chats/{chat_id}/messages/{message_id}/report` and `POST /groups/{chat_id}/report` `{reason, note?}` → `204`; `DELETE` on the same paths withdraws (`withdrawn_at`, as for user reports).
- Reasons: those of user reports plus `illegal`, a notice of illegal content under the Digital Services Act, which needs a `note` explaining why (`422` without it). Notes are at most 1000 characters.
- A message report copies the reported message and up to 5 messages before and after it **as the reporter sees the chat** (`messages.Service.ReportWindow`: messages hidden for the reporter, deleted ones, system messages and those before their cleared history are left out). Each copied message keeps its id, sender id, display name and username, type, text or caption, file names, forwarded-from name, reply id, time, edit time and whether it is the reported one. The chat's type and, for groups, title go with it. The sender is stored as `target_id`.
- A group report copies the title, description, avatar attachment id, creator, owner and member count; only members can report a group (`404` otherwise and for direct chats). The owner is `target_id`.
- One active report per reporter and message (or group): reporting again changes the reason and the note but keeps the first copy and `created_at`. Own messages and system messages are `422`; a message the reporter cannot see is `404`.
- User, message and group reports share the limit of 20 per reporter in 24 hours (`429`); updates and withdrawals count, as for user reports.
- Content reports do not block anyone and do not change the sender's automatic account status; a moderator decides. Each new report counts `messenger_content_reports_total{kind, reason}`.
- Retention: the daily moderation job deletes content reports 6 months after `created_at` (`contentreports.Service.Purge`). When the reporter deletes their account, `reporter_id` becomes `NULL` and the copy stays until then.

## Removed by moderation

A moderator's decision can remove a message (`messages.Service.RemoveByModeration`, called by the moderation service through `POST /internal/v1/chats/{chat_id}/messages/{message_id}/remove`, [internal-api.md](internal-api.md#remove-a-message)). The message is deleted for everyone like a sender's delete (content and files cleared, files unlinked, `message.deleted` event for search) and also stores `removed_by_moderation` with the reason (`moderation_reason_code`, `moderation_reason_text`; ScyllaDB migration `000004_moderation_removal`). Removing twice keeps the first reason.

- History, reply previews and `message` renderings carry `removed_by_moderation: true`; clients show "Removed by moderation" instead of the deleted placeholder. Older clients still see a deleted message.
- Only the sender's rendering carries `moderation_reason` (`code`, `text`).
- Members get the WebSocket `delete` event with `scope: everyone` and `removed_by_moderation: true`; the sender's copy adds `moderation_reason`.

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

A staff decision changes these rules for the account ([Staff decisions](#staff-decisions)).

Every change invalidates the cached status, counts `messenger_moderation_status_changes_total{status}` and sends the WebSocket event `account_status` (an `AccountStatus`) to the user's own sessions.

## Staff decisions

The moderation service applies them through the service functions below (exposed on the internal listener, not on the public API). It keeps its own audit log and cases; Messenger keeps the rules, the status history and the live updates.

- `Service.Decide` sets any status with a reason code (`spam`, `scam`, `harassment`, `impersonation`, `inappropriate_profile`, `illegal`, `other`), a text for the user (1 to 1000 characters) and an optional `staff_ref` (up to 64 printable ASCII characters, the moderation service's reference). It writes `decided_by: staff`, `decided_at`, the reason, ends a running probation and adds a status history row with cause `manual`, also when the level stays the same (confirming an automatic level). `since` changes only when the level changes.
- Only a person lowers or undoes a staff decision: decay, the quiet step, the automatic appeal (`can_appeal: false`) and reports made before the decision never change it. Only new weighted reports, made after the decision and scored on their own, can raise it: when they call for a higher level, the status rises at once (`cause: reports`) and the decision fields are cleared, and a heavy level opens a case in the moderation service for a person to confirm or reverse, like any automatic `restricted` or `suspended` level.
- `Service.DismissReports` sets `dismissed_at` on the active reports of an account (all, or those of the given reporters). Dismissed reports stop counting and stay dismissed when their reporter reports again. The status is recomputed at once; a lower level is recorded with cause `dismissed`. A staff decision is not lowered by a dismissal.
- `Service.HidePack` / `UnhidePack` hide or restore a sticker or emoji pack (`hidden_by: staff`; automatic hiding after 5 reports writes `hidden_by: reports`). Unhiding clears the pack's reports. Built-in sticker packs cannot be hidden.
- `auth.Service.DeleteAccountByStaff` deletes an account like `DELETE /me` without the password and ends every session.

`AccountStatus` carries `decided_by`, `reason_code` and `reason_text`; the app shows a staff decision as "Reviewed by a person" from "Messenger moderation", never with a person's name.

### Report outcomes

Each decision tells the reporters of the subject once per report (`report_outcomes`, kept 90 days, deleted by the daily job): `action_taken` for a decision above `good` and for hiding a pack, `no_violation` for `good`, dismissals and unhiding. A reporter already told about a report is not told again until they report anew. The reporter gets the WebSocket event `report_outcome`, `GET /me/report-outcomes` lists the last 50 and `POST /me/report-outcomes/{outcome_id}/read` marks one as read. The notice never says what action was taken.

## Review by a person

`POST /me/account-status/review` `{text}` → `200` `AccountStatus`: the user asks the moderation team to look at their account (GDPR Art. 22, DSA Art. 20). The back end only stores the request; the moderation service (`messenger-moderation`) reads `account_review_requests` with its read-only database user, opens a case for it and sends the decision back through the internal endpoints.

- Allowed while the status is not `good` and no request of the user is open, otherwise `409`. The partial unique index `account_review_requests_open_key` keeps it at one open request even under concurrent calls.
- At most 3 requests in 30 days, closed ones included (`429`).
- `text` is required: trimmed, 1 to 500 characters, no control characters except line breaks (`422`). The row also keeps the status at the time of the request.
- `AccountStatus.review_requested_at` is the time of the open request (`null` when none), `can_request_review` tells clients whether the button works now. Both are also in the WebSocket event `account_status`, which goes to the user's own sessions after a request too.
- A request closes (`closed_at`, `close_reason`) when a person decides (`decided`, set by `Service.Decide`, which the moderation service calls through `POST /internal/v1/users/{user_id}/status`) or when the status returns to `good` on its own (`status_good`, after a decay, the quiet step down or an appeal). Appeals and review requests are independent: a user may do both.

## Enforcement

`moderation.Service.Allow(ctx, actor, action, target)` is the single check. It returns `*moderation.ErrRestricted{Action}`, which the HTTP layer answers with `403` `account_restricted` and `restriction`, and the WebSocket with an `error` event carrying the same fields.

| Action | Checked in |
|---|---|
| `start_direct` | `chats.CreateDirect` (new chats and requests only, not reopening an existing chat) |
| `invite_to_group` | `chats.CreateGroup` (each initial member), `chats.AddMember` |
| `create_group` | `chats.CreateGroup` |
| `call` | `calls.Start` (direct: the peer; group: no target); `calls.Join` (suspended only) |
| `manage_group` | `chats.UpdateGroup`, `SetGroupAvatar`, `RemoveGroupAvatar`, `SetGroupBackground`, `SetRole`, `TransferOwnership`, `RemoveMember` of someone else (suspended only) |
| `send` | `messages.Send`, `Resend`, `Edit`, `React`, `Vote`, `ForwardMany`; `Typing` drops the indicator |
| `forward` | `messages.ForwardMany` when a forwarded message has a file (stickers excluded) |
| `upload` | `POST /attachments`, `POST /upload-sessions` (`httpapi.Server`) |
| `edit_profile` | `PATCH /me` with `display_name` or `username`, `PUT /me/avatar`, `PUT /me/music`, `PUT /me/background` |
| `publish_pack` | `POST /stickers/packs`, `POST /stickers/packs/{id}/stickers`, `POST /emoji/packs`, `POST /emoji/packs/{id}/emoji` |
| `search` | `GET /users?q=` leaves out `restricted` and `suspended` users (except the viewer) |

The status is cached in Redis (`moderation:{user_id}`, 1 minute) and read from PostgreSQL on a miss. When neither answers, `Allow` fails closed (`503` with `Retry-After`, `internal_error` on the WebSocket), like the membership cache. `account_label` comes from the same cache (`Service.Labels`, one `MGET` per rendering; misses are read from PostgreSQL in one query and cached, `good` included), and is left out when neither answers.

`chats.Service.MustBeRequest(requester, recipient)` tells whether a new direct chat must land in the recipient's Requests folder: true for `limited` and worse, and when the status cannot be read. `CreateDirect` then opens the chat with `is_request` set for the recipient even when the requester is their contact ([chats.md](chats.md#requests-folder)).

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

- `content_reports (id, reporter_id, kind, chat_id, message_id, target_id, reason, note, content JSONB, created_at, updated_at, withdrawn_at)`: `kind` `message` or `group` (`message_id` 0); unique `(reporter_id, chat_id, message_id)`; `reporter_id` and `target_id` `ON DELETE SET NULL`; no foreign key on the chat, so the copy outlives a deleted chat until the 6 months are over.

- `user_reports (reporter_id, target_id, reason, note, context, weight NUMERIC(3,2), snapshot JSONB, created_at, updated_at, withdrawn_at)`, primary key `(reporter_id, target_id)`, indexes `(target_id, created_at)` and `(reporter_id, created_at)`. Both users `ON DELETE CASCADE`.
- `user_reports.dismissed_at`: set when the moderation team dismissed the report.
- `user_moderation (user_id, status, score, status_since, probation_until, appeal_blocked_until, last_appeal_at, decided_by, decided_at, reason_code, reason_text)`: one row per user who was ever recomputed, appealed or decided on; no row means `good`.
- `user_moderation_events`: the history of status changes, with `reason_code`, `reason_text` and `staff_ref` for manual decisions. The moderation service reads it to open cases for automatic `restricted` and `suspended` levels.
- `sticker_packs.hidden_by`, `emoji_packs.hidden_by`: `reports` or `staff` while the pack is hidden.
- `report_outcomes (id, reporter_id, subject_type, subject_id, outcome, created_at, read_at)`.
- `account_review_requests (id, user_id, status, text, created_at, closed_at, close_reason)`: requests for a review by a person; at most one open per user. Migration `000047_account_review_requests`.
