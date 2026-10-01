import json

from server.time_utils import to_utc_iso


def _parse_list(value) -> list:
    if not value:
        return []
    if isinstance(value, list):
        return value
    try:
        parsed = json.loads(value)
    except (TypeError, json.JSONDecodeError):
        return []
    return parsed if isinstance(parsed, list) else []


def message_visible_to(message, user_id: int) -> bool:
    keys = message.keys()
    deleted_for = message["deleted_for"] if "deleted_for" in keys else None
    if user_id in _parse_list(deleted_for):
        return False
    if message["sender_id"] == user_id:
        return True
    undelivered_to = message["undelivered_to"] if "undelivered_to" in keys else None
    return user_id not in _parse_list(undelivered_to)


def _message_type_and_content(content: str) -> tuple[str, object]:
    if content and content.startswith("{"):
        try:
            parsed = json.loads(content)
        except json.JSONDecodeError:
            return "message", content
        if isinstance(parsed, dict) and parsed.get("file_url"):
            return "file", parsed
    return "message", content


def get_message_summary(cursor, message_id: int, requester_id: int | None = None) -> dict | None:
    cursor.execute(
        """
        SELECT id, chat_id, sender_id, sender_name, content, timestamp, edited_at,
               reactions, read_by, delivery_error, undelivered_to, deleted_for
        FROM messages
        WHERE id = ?
        """,
        (message_id,),
    )
    message = cursor.fetchone()
    if not message or (requester_id is not None and not message_visible_to(message, requester_id)):
        return None
    message_type, content = _message_type_and_content(message["content"])
    return {
        "id": message["id"],
        "chat_id": message["chat_id"],
        "sender_id": message["sender_id"],
        "sender_name": message["sender_name"],
        "content": content,
        "type": message_type,
        "timestamp": to_utc_iso(message["timestamp"]),
        "edited_at": to_utc_iso(message["edited_at"]),
        "reactions": _parse_list(message["reactions"]),
        "read_by": _parse_list(message["read_by"]),
        "delivery_error": message["delivery_error"],
    }


def _latest_visible_message_id(cursor, chat_id: int, user_id: int, batch_size: int = 50) -> int | None:
    before_id = None
    while True:
        query = "SELECT id, sender_id, undelivered_to, deleted_for FROM messages WHERE chat_id = ?"
        params = [chat_id]
        if before_id is not None:
            query += " AND id < ?"
            params.append(before_id)
        cursor.execute(f"{query} ORDER BY id DESC LIMIT ?", (*params, batch_size))
        rows = cursor.fetchall()
        for row in rows:
            if message_visible_to(row, user_id):
                return row["id"]
        if len(rows) < batch_size:
            return None
        before_id = rows[-1]["id"]


def get_chat_unread_summary(cursor, chat_id: int, user_id: int) -> dict:
    cursor.execute(
        """
        SELECT id, sender_id, read_by, undelivered_to, deleted_for
        FROM messages
        WHERE chat_id = ? AND sender_id != ?
        ORDER BY id ASC
        """,
        (chat_id, user_id),
    )
    unread_ids = []
    for message in cursor.fetchall():
        if not message_visible_to(message, user_id):
            continue
        if any(
            isinstance(read, dict) and (read.get("user_id") or read.get("id")) == user_id
            for read in _parse_list(message["read_by"])
        ):
            continue
        unread_ids.append(message["id"])

    latest_message_id = _latest_visible_message_id(cursor, chat_id, user_id)
    return {
        "last_message": (
            get_message_summary(cursor, latest_message_id, user_id)
            if latest_message_id
            else None
        ),
        "unread_count": len(unread_ids),
        "first_unread_message_id": unread_ids[0] if unread_ids else None,
    }
