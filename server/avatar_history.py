from urllib.parse import quote

from server.database import get_connection
from server.time_utils import utc_now_iso


def make_avatar_url(username: str, filename: str) -> str:
    return f"/static/avatars/{quote(username, safe='')}/{quote(filename, safe='')}"


def record_user_avatar(user_id: int, avatar_url: str) -> None:
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("UPDATE users SET avatar_url = ? WHERE id = ?", (avatar_url, user_id))
        cursor.execute("UPDATE user_avatar_history SET is_current = 0 WHERE user_id = ?", (user_id,))
        cursor.execute(
            """
            INSERT INTO user_avatar_history (user_id, avatar_url, created_at, is_current)
            VALUES (?, ?, ?, 1)
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
