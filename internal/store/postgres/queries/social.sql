-- Privacy settings, exceptions, blocks and contact names: what one user lets others see and do.

-- name: ListPrivacySettings :many
SELECT * FROM user_privacy_settings WHERE user_id = ANY(@user_ids::BIGINT[]);

-- name: UpdatePrivacySettings :one
INSERT INTO user_privacy_settings (
    user_id, avatar_visibility, profile_visibility, presence_visibility, read_receipts_enabled,
    direct_messages, group_invites, search_visibility
) VALUES (
    @user_id, @avatar_visibility, @profile_visibility, @presence_visibility, @read_receipts_enabled,
    @direct_messages, @group_invites, @search_visibility
)
ON CONFLICT (user_id) DO UPDATE SET
    avatar_visibility = EXCLUDED.avatar_visibility,
    profile_visibility = EXCLUDED.profile_visibility,
    presence_visibility = EXCLUDED.presence_visibility,
    read_receipts_enabled = EXCLUDED.read_receipts_enabled,
    direct_messages = EXCLUDED.direct_messages,
    group_invites = EXCLUDED.group_invites,
    search_visibility = EXCLUDED.search_visibility
RETURNING *;

-- name: ListPrivacyExceptions :many
SELECT * FROM user_privacy_exceptions WHERE owner_id = @owner_id ORDER BY setting_key, created_at;

-- name: ListPrivacyExceptionsForTarget :many
-- The exceptions that several owners made for one user (what may that user see of each of them).
SELECT * FROM user_privacy_exceptions WHERE owner_id = ANY(@owner_ids::BIGINT[]) AND target_user_id = @target_user_id;

-- name: ListPrivacyExceptionsOfOwnerFor :many
-- The exceptions one owner made for several users (what may each of them see of the owner).
SELECT * FROM user_privacy_exceptions WHERE owner_id = @owner_id AND target_user_id = ANY(@target_ids::BIGINT[]);

-- name: SetPrivacyException :exec
INSERT INTO user_privacy_exceptions (owner_id, setting_key, target_user_id, effect)
VALUES (@owner_id, @setting_key, @target_user_id, @effect)
ON CONFLICT (owner_id, setting_key, target_user_id) DO UPDATE SET effect = EXCLUDED.effect;

-- name: DeletePrivacyException :execrows
DELETE FROM user_privacy_exceptions WHERE owner_id = @owner_id AND setting_key = @setting_key AND target_user_id = @target_user_id;

-- name: BlockUser :exec
INSERT INTO user_blocks (blocker_id, blocked_id) VALUES (@blocker_id, @blocked_id) ON CONFLICT DO NOTHING;

-- name: UnblockUser :execrows
DELETE FROM user_blocks WHERE blocker_id = @blocker_id AND blocked_id = @blocked_id;

-- name: ListBlocked :many
SELECT * FROM user_blocks WHERE blocker_id = @blocker_id ORDER BY created_at DESC;

-- name: BlockedEither :one
-- Whether a blocked b or b blocked a.
SELECT EXISTS (
    SELECT 1 FROM user_blocks
    WHERE (blocker_id = @a AND blocked_id = @b) OR (blocker_id = @b AND blocked_id = @a)
);

-- name: SetContactName :exec
INSERT INTO user_contact_names (owner_id, target_id, display_name) VALUES (@owner_id, @target_id, @display_name)
ON CONFLICT (owner_id, target_id) DO UPDATE SET display_name = EXCLUDED.display_name;

-- name: DeleteContactName :execrows
DELETE FROM user_contact_names WHERE owner_id = @owner_id AND target_id = @target_id;

-- name: ListContactNames :many
SELECT * FROM user_contact_names WHERE owner_id = @owner_id AND target_id = ANY(@target_ids::BIGINT[]);

-- name: ListContactNamesForTarget :many
-- The names several owners gave one user (how each of them sees the target).
SELECT * FROM user_contact_names WHERE owner_id = ANY(@owner_ids::BIGINT[]) AND target_id = @target_id;

-- name: SharedChatPartners :many
-- Which of the users share at least one chat with the viewer.
SELECT DISTINCT other.user_id
FROM participants mine
JOIN participants other ON other.chat_id = mine.chat_id
WHERE mine.user_id = @viewer_id AND other.user_id = ANY(@user_ids::BIGINT[]);

-- name: ListChatIDsOfUser :many
SELECT chat_id FROM participants WHERE user_id = @user_id;
