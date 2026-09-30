from server.presence import is_user_online
from server.time_utils import to_utc_iso


DEFAULT_AVATAR = "/static/avatars/default.jpg"
PERMISSION_ALLOWED = "allowed"
PERMISSION_APPROVAL_REQUIRED = "approval_required"
PERMISSION_DENIED = "denied"

AVATAR_PROFILE_VISIBILITY_SCOPES = {
    "everyone",
    "shared_chats",
    "everyone_except",
    "nobody_except",
}
PRESENCE_VISIBILITY_SCOPES = AVATAR_PROFILE_VISIBILITY_SCOPES | {"nobody"}
DIRECT_MESSAGE_VISIBILITY_SCOPES = {"everyone", "shared_chats", "wait_approval"}
GROUP_INVITE_VISIBILITY_SCOPES = PRESENCE_VISIBILITY_SCOPES | {"wait_approval"}
SEARCH_VISIBILITY = {"everyone", "nobody"}
PRIVACY_EXCEPTION_KEYS = {
    "avatar_visibility",
    "profile_visibility",
    "presence_visibility",
    "group_invites",
}
PRIVACY_EXCEPTION_EFFECTS = {"allow", "deny"}
DEFAULT_PRIVACY_SETTINGS = {
    "avatar_visibility": "everyone",
    "profile_visibility": "everyone",
    "presence_visibility": "everyone",
    "read_receipts_enabled": True,
    "direct_messages": "everyone",
    "group_invites": "everyone",
    "search_visibility": "everyone",
}


def normalize_visibility(value: str | None, allowed_values: set[str]) -> str:
    normalized = (value or "").strip().lower()
    if normalized not in allowed_values:
        raise ValueError("Unsupported privacy visibility")
    return normalized


def ensure_privacy_settings(cursor, user_id: int) -> None:
    cursor.execute("INSERT OR IGNORE INTO user_privacy_settings (user_id) VALUES (?)", (user_id,))


def get_privacy_settings(cursor, user_id: int, include_exceptions: bool = False) -> dict:
    ensure_privacy_settings(cursor, user_id)
    cursor.execute("SELECT * FROM user_privacy_settings WHERE user_id = ?", (user_id,))
    row = cursor.fetchone()
    settings = {
        key: (
            bool(row[key])
            if key == "read_receipts_enabled"
            else row[key]
        )
        for key in DEFAULT_PRIVACY_SETTINGS
    }
    if not include_exceptions:
        return settings

    exceptions = {
        key: {"allow": [], "deny": []}
        for key in PRIVACY_EXCEPTION_KEYS
    }
    cursor.execute(
        """
        SELECT e.setting_key, e.effect, u.id, u.username, u.display_name,
               u.avatar_url, u.bio, u.created_at, u.last_seen
        FROM user_privacy_exceptions e
        JOIN users u ON u.id = e.target_user_id
        WHERE e.owner_id = ?
        ORDER BY u.username COLLATE NOCASE
        """,
        (user_id,),
    )
    for row in cursor.fetchall():
        if row["setting_key"] not in exceptions or row["effect"] not in PRIVACY_EXCEPTION_EFFECTS:
            continue
        exceptions[row["setting_key"]][row["effect"]].append(
            serialize_user(cursor, row, user_id)
        )
    settings["privacy_exceptions"] = exceptions
    return settings


def _shares_chat(cursor, first_user_id: int, second_user_id: int) -> bool:
    cursor.execute(
        """
        SELECT 1
        FROM participants first_participant
        JOIN participants second_participant
          ON second_participant.chat_id = first_participant.chat_id
        WHERE first_participant.user_id = ?
          AND second_participant.user_id = ?
        LIMIT 1
        """,
        (first_user_id, second_user_id),
    )
    return cursor.fetchone() is not None


def _exception_effect(cursor, owner_id: int, setting_key: str, requester_id: int) -> str | None:
    cursor.execute(
        """
        SELECT effect
        FROM user_privacy_exceptions
        WHERE owner_id = ? AND setting_key = ? AND target_user_id = ?
        """,
        (owner_id, setting_key, requester_id),
    )
    row = cursor.fetchone()
    return row["effect"] if row else None


def visibility_allows(
    cursor,
    requester_id: int | None,
    owner_id: int,
    visibility: str,
    setting_key: str,
) -> bool:
    if requester_id is None or requester_id == owner_id:
        return True

    effect = _exception_effect(cursor, owner_id, setting_key, requester_id)
    if effect == "allow":
        return True
    if effect == "deny":
        return False
    if visibility in {"everyone", "everyone_except"}:
        return True
    if visibility in {"nobody", "nobody_except"}:
        return False
    if visibility == "shared_chats":
        return _shares_chat(cursor, requester_id, owner_id)
    return False


def is_blocked_between(cursor, first_user_id: int, second_user_id: int) -> bool:
    cursor.execute(
        """
        SELECT 1
        FROM user_blocks
        WHERE (blocker_id = ? AND blocked_id = ?)
           OR (blocker_id = ? AND blocked_id = ?)
        LIMIT 1
        """,
        (first_user_id, second_user_id, second_user_id, first_user_id),
    )
    return cursor.fetchone() is not None


def get_direct_chat_id(cursor, first_user_id: int, second_user_id: int) -> int | None:
    cursor.execute(
        """
        SELECT id
        FROM chats
        WHERE type = 'one-on-one'
          AND (
            (user1_id = ? AND user2_id = ?)
            OR (user1_id = ? AND user2_id = ?)
          )
        """,
        (first_user_id, second_user_id, second_user_id, first_user_id),
    )
    row = cursor.fetchone()
    return row["id"] if row else None


def _permission_for_visibility(cursor, requester_id: int, owner_id: int, visibility: str, setting_key: str) -> str:
    if requester_id == owner_id:
        return PERMISSION_ALLOWED
    if is_blocked_between(cursor, requester_id, owner_id):
        return PERMISSION_DENIED
    if visibility_allows(cursor, requester_id, owner_id, visibility, setting_key):
        return PERMISSION_ALLOWED
    return PERMISSION_APPROVAL_REQUIRED if visibility == "wait_approval" else PERMISSION_DENIED


def direct_chat_permission(cursor, requester_id: int, recipient_id: int) -> str:
    settings = get_privacy_settings(cursor, recipient_id)
    return _permission_for_visibility(
        cursor,
        requester_id,
        recipient_id,
        settings["direct_messages"],
        "direct_messages",
    )


def group_invite_permission(cursor, requester_id: int, recipient_id: int) -> str:
    settings = get_privacy_settings(cursor, recipient_id)
    return _permission_for_visibility(
        cursor,
        requester_id,
        recipient_id,
        settings["group_invites"],
        "group_invites",
    )


def can_send_to_chat(cursor, chat_id: int, sender_id: int) -> bool:
    cursor.execute("SELECT type, user1_id, user2_id FROM chats WHERE id = ?", (chat_id,))
    chat = cursor.fetchone()
    if not chat:
        return False
    if chat["type"] != "one-on-one":
        cursor.execute(
            "SELECT 1 FROM participants WHERE chat_id = ? AND user_id = ?",
            (chat_id, sender_id),
        )
        return cursor.fetchone() is not None
    recipient_id = chat["user2_id"] if chat["user1_id"] == sender_id else chat["user1_id"]
    return recipient_id is not None and direct_chat_permission(cursor, sender_id, recipient_id) == PERMISSION_ALLOWED


def read_receipts_enabled(cursor, user_id: int) -> bool:
    return bool(get_privacy_settings(cursor, user_id)["read_receipts_enabled"])


def can_target_be_searched(cursor, requester_id: int, target_id: int) -> bool:
    settings = get_privacy_settings(cursor, target_id)
    return requester_id == target_id or settings["search_visibility"] == "everyone"


def _value(row, key: str, default=None):
    try:
        return row[key]
    except (KeyError, IndexError):
        return default


def serialize_user(cursor, user, requester_id: int | None = None) -> dict:
    user_id = _value(user, "id")
    username = _value(user, "username", "")
    settings = get_privacy_settings(cursor, user_id)
    profile_visible = visibility_allows(
        cursor, requester_id, user_id, settings["profile_visibility"], "profile_visibility"
    )
    avatar_visible = visibility_allows(
        cursor, requester_id, user_id, settings["avatar_visibility"], "avatar_visibility"
    )
    presence_visible = visibility_allows(
        cursor, requester_id, user_id, settings["presence_visibility"], "presence_visibility"
    )

    contact_display_name = None
    if requester_id is not None and requester_id != user_id:
        cursor.execute(
            "SELECT display_name FROM user_contact_names WHERE owner_id = ? AND target_id = ?",
            (requester_id, user_id),
        )
        contact = cursor.fetchone()
        contact_display_name = contact["display_name"] if contact else None

    display_name = _value(user, "display_name", username) if profile_visible else username
    account_display_name = _value(user, "display_name", username)
    return {
        "id": user_id,
        "username": username,
        "display_name": contact_display_name or display_name,
        "account_display_name": account_display_name,
        "contact_display_name": contact_display_name,
        "avatar_url": (_value(user, "avatar_url") or DEFAULT_AVATAR) if avatar_visible else DEFAULT_AVATAR,
        "bio": _value(user, "bio", "") if profile_visible else "",
        "created_at": to_utc_iso(_value(user, "created_at")),
        "is_online": is_user_online(user_id) if presence_visible else False,
        "last_seen": to_utc_iso(_value(user, "last_seen")) if presence_visible else None,
        "can_message": (
            direct_chat_permission(cursor, requester_id, user_id) == PERMISSION_ALLOWED
            if requester_id is not None and requester_id != user_id
            else True
        ),
        "direct_chat_id": (
            get_direct_chat_id(cursor, requester_id, user_id)
            if requester_id is not None and requester_id != user_id
            else None
        ),
        "direct_message_reason": (
            "blocked"
            if requester_id is not None
            and requester_id != user_id
            and is_blocked_between(cursor, requester_id, user_id)
            else None
        ),
    }


def serialize_user_snapshot(cursor, user_id: int, requester_id: int | None = None) -> dict:
    cursor.execute(
        """
        SELECT id, username, display_name, avatar_url, bio, created_at, last_seen
        FROM users
        WHERE id = ?
        """,
        (user_id,),
    )
    user = cursor.fetchone()
    if not user:
        return {
            "id": user_id,
            "user_id": user_id,
            "username": "Deleted User",
            "display_name": "Deleted User",
            "avatar_url": DEFAULT_AVATAR,
            "is_online": False,
            "last_seen": None,
        }
    return {**serialize_user(cursor, user, requester_id), "user_id": user_id}
