"""Authorization rules for files under /static (chat attachments, voice messages, avatars)."""
import json
from pathlib import PurePosixPath

from server.chat_summary import message_visible_to
from server.privacy import get_privacy_settings, visibility_allows

UPLOADS_DIR = "uploads"
VOICE_DIR = "vm"
AVATARS_DIR = "avatars"
GROUP_AVATARS_DIR = "groups"

# Placeholders shown to everyone, including before login.
PUBLIC_FILES = frozenset({
    "avatars/default.jpg",
    "avatars/group.png",
    "avatars/deleted.jpg",
})

ATTACHMENT_DIRS = (UPLOADS_DIR, VOICE_DIR)


def normalize_media_path(raw_path: str) -> str | None:
    """Return a clean relative path like 'uploads/x.png', or None if it is not a plain file path."""
    if not raw_path or "\x00" in raw_path or "\\" in raw_path:
        return None
    parts = PurePosixPath(raw_path).parts
    if not parts or any(part in ("", ".", "..") or part.startswith("/") for part in parts):
        return None
    return "/".join(parts)


def is_public_media(rel_path: str) -> bool:
    return rel_path in PUBLIC_FILES


def is_attachment_path(rel_path: str) -> bool:
    parts = rel_path.split("/")
    return len(parts) == 2 and parts[0] in ATTACHMENT_DIRS


def attachment_url(rel_path: str) -> str:
    return f"/static/{rel_path}"


def attachment_path_from_url(file_url) -> str | None:
    """Map a stored '/static/uploads/x' style URL to its relative media path, if it is an attachment."""
    if not isinstance(file_url, str) or not file_url.startswith("/static/"):
        return None
    rel_path = normalize_media_path(file_url[len("/static/"):])
    return rel_path if rel_path and is_attachment_path(rel_path) else None


def record_attachment(cursor, message_id: int, file_url) -> None:
    """Bind a file to the message that carries it; only these rows grant access to the file."""
    rel_path = attachment_path_from_url(file_url)
    if rel_path:
        cursor.execute(
            "INSERT OR IGNORE INTO message_attachments (message_id, file_path) VALUES (?, ?)",
            (message_id, rel_path),
        )


def copy_attachments(cursor, source_message_id: int, target_message_id: int) -> None:
    cursor.execute(
        """
        INSERT INTO message_attachments (message_id, file_path)
        SELECT ?, file_path FROM message_attachments WHERE message_id = ?
        """,
        (target_message_id, source_message_id),
    )


def backfill_message_attachments(cursor) -> None:
    """One-off for messages stored before attachments were tracked; a no-op once they are all recorded."""
    cursor.execute(
        """
        SELECT m.id, m.content
        FROM messages m
        WHERE m.content LIKE '{%"file_url"%'
          AND NOT EXISTS (SELECT 1 FROM message_attachments a WHERE a.message_id = m.id)
        """
    )
    for row in cursor.fetchall():
        try:
            content = json.loads(row["content"])
        except (TypeError, ValueError):
            continue
        if isinstance(content, dict):
            record_attachment(cursor, row["id"], content.get("file_url"))


def can_access_attachment(cursor, user_id: int, rel_path: str) -> bool:
    """True if a message the user can still see, in a chat they belong to, carries the file."""
    cursor.execute(
        """
        SELECT m.sender_id, m.undelivered_to, m.deleted_for
        FROM message_attachments a
        JOIN messages m ON m.id = a.message_id
        JOIN participants p ON p.chat_id = m.chat_id AND p.user_id = ?
        WHERE a.file_path = ?
        """,
        (user_id, rel_path),
    )
    return any(message_visible_to(message, user_id) for message in cursor.fetchall())


def can_access_user_avatar(cursor, viewer_id: int, username: str) -> bool:
    cursor.execute("SELECT id FROM users WHERE username = ?", (username,))
    owner = cursor.fetchone()
    if not owner:
        return False
    if owner["id"] == viewer_id:
        return True
    settings = get_privacy_settings(cursor, owner["id"])
    return visibility_allows(cursor, viewer_id, owner["id"], settings["avatar_visibility"], "avatar_visibility")


def can_access_group_avatar(cursor, viewer_id: int, rel_path: str) -> bool:
    cursor.execute(
        "SELECT id FROM chats WHERE type = 'group' AND avatar_url = ?",
        (attachment_url(rel_path),),
    )
    for chat in cursor.fetchall():
        cursor.execute(
            "SELECT 1 FROM participants WHERE chat_id = ? AND user_id = ?",
            (chat["id"], viewer_id),
        )
        if cursor.fetchone():
            return True
        cursor.execute(
            """
            SELECT 1 FROM approval_requests
            WHERE type = 'group_invite' AND chat_id = ? AND recipient_id = ? AND status = 'pending'
            """,
            (chat["id"], viewer_id),
        )
        if cursor.fetchone():
            return True
    return False


def can_access_media(cursor, user_id: int, rel_path: str) -> bool:
    parts = rel_path.split("/")
    if is_attachment_path(rel_path):
        return can_access_attachment(cursor, user_id, rel_path)
    if parts[0] == AVATARS_DIR and len(parts) == 3 and parts[1] == GROUP_AVATARS_DIR:
        return can_access_group_avatar(cursor, user_id, rel_path)
    if parts[0] == AVATARS_DIR and len(parts) == 3:
        return can_access_user_avatar(cursor, user_id, parts[1])
    return False
