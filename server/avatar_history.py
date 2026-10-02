from pathlib import Path
import secrets
from urllib.parse import quote

from server.database import get_connection
from server.time_utils import utc_now_iso


def make_avatar_url(username: str, filename: str) -> str:
    return f"/static/avatars/{quote(username, safe='')}/{quote(filename, safe='')}"


AVATARS_ROOT = Path("static/avatars")


def avatar_dir_name(user_id: int) -> str:
    # "-" is not allowed in usernames, so these directories can never collide with a username directory.
    return f"id-{int(user_id)}"


def store_user_avatar(user_id: int, image_bytes: bytes, extension: str) -> str:
    """Write an avatar under a directory derived from the user id and return its URL."""
    root = AVATARS_ROOT.resolve()
    directory = (root / avatar_dir_name(user_id)).resolve()
    filename = f"{secrets.token_hex(16)}{extension}"
    target = (directory / filename).resolve()
    if not target.is_relative_to(root) or target.parent != directory:
        raise ValueError("Avatar path escapes the upload directory")
    directory.mkdir(parents=True, exist_ok=True)
    target.write_bytes(image_bytes)
    return make_avatar_url(avatar_dir_name(user_id), filename)


def record_user_avatar(user_id: int, avatar_url: str) -> None:
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("UPDATE users SET avatar_url = ? WHERE id = ?", (avatar_url, user_id))
        cursor.execute("UPDATE user_avatar_history SET is_current = FALSE WHERE user_id = ?", (user_id,))
        cursor.execute(
            """
            INSERT INTO user_avatar_history (user_id, avatar_url, created_at, is_current)
            VALUES (?, ?, ?, TRUE)
            """,
            (user_id, avatar_url, utc_now_iso()),
        )
        conn.commit()
    finally:
        conn.close()


def get_avatar_history(username: str) -> dict | None:
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("SELECT id FROM users WHERE username = ?", (username,))
        user = cursor.fetchone()
        if not user:
            return None
        cursor.execute(
            """
            SELECT id, avatar_url, created_at, is_current
            FROM user_avatar_history
            WHERE user_id = ?
            ORDER BY is_current DESC, created_at DESC, id DESC
            """,
            (user["id"],),
        )
        return {
            "avatars": [
                {
                    "id": row["id"],
                    "avatar_url": row["avatar_url"],
                    "created_at": row["created_at"],
                    "is_current": bool(row["is_current"]),
                }
                for row in cursor.fetchall()
            ]
        }
    finally:
        conn.close()
