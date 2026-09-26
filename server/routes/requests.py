from fastapi import APIRouter, Depends, HTTPException

from server.database import get_connection
from server.privacy import DEFAULT_AVATAR, serialize_user
from server.routes.auth import get_current_user
from server.time_utils import to_utc_iso, utc_now_iso


router = APIRouter()


def create_direct_message_request(cursor, requester_id: int, recipient_id: int, message_text: str):
    cursor.execute(
        """
        SELECT id
        FROM approval_requests
        WHERE type = 'direct_message'
          AND requester_id = ?
          AND recipient_id = ?
          AND status = 'pending'
        """,
        (requester_id, recipient_id),
    )
    existing = cursor.fetchone()
    if existing:
        return existing["id"], True
    cursor.execute(
        """
        INSERT INTO approval_requests (type, requester_id, recipient_id, message_text, created_at)
        VALUES ('direct_message', ?, ?, ?, ?)
        """,
        (requester_id, recipient_id, message_text.strip(), utc_now_iso()),
    )
    return cursor.lastrowid, False


def create_group_invite_request(cursor, requester_id: int, recipient_id: int, chat_id: int) -> int:
    cursor.execute(
        """
        SELECT id
        FROM approval_requests
        WHERE type = 'group_invite'
          AND requester_id = ?
          AND recipient_id = ?
          AND chat_id = ?
          AND status = 'pending'
        """,
        (requester_id, recipient_id, chat_id),
    )
    existing = cursor.fetchone()
    if existing:
        return existing["id"]
    cursor.execute(
        """
        INSERT INTO approval_requests (type, requester_id, recipient_id, chat_id, created_at)
        VALUES ('group_invite', ?, ?, ?, ?)
        """,
        (requester_id, recipient_id, chat_id, utc_now_iso()),
    )
    return cursor.lastrowid


def _load_request(cursor, request_id: int, recipient_id: int):
    cursor.execute(
        """
        SELECT *
        FROM approval_requests
        WHERE id = ? AND recipient_id = ?
        """,
        (request_id, recipient_id),
    )
    request = cursor.fetchone()
    if not request:
        raise HTTPException(status_code=404, detail="Approval request not found")
    if request["status"] != "pending":
        raise HTTPException(status_code=400, detail="Approval request has already been handled")
    return request


def _create_direct_chat(cursor, request) -> int:
    requester_id = request["requester_id"]
    recipient_id = request["recipient_id"]
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
        (requester_id, recipient_id, recipient_id, requester_id),
    )
    existing = cursor.fetchone()
    if existing:
        return existing["id"]

    cursor.execute(
        """
        SELECT id, username, display_name
        FROM users
        WHERE id IN (?, ?)
        """,
        (requester_id, recipient_id),
    )
    users = {row["id"]: row for row in cursor.fetchall()}
    if requester_id not in users or recipient_id not in users:
        raise HTTPException(status_code=404, detail="A request participant no longer exists")

    cursor.execute(
        """
        INSERT INTO chats (name, type, user1_id, user2_id)
        VALUES (?, 'one-on-one', ?, ?)
        """,
        (
            f"{users[requester_id]['username']} & {users[recipient_id]['username']}",
            requester_id,
            recipient_id,
        ),
    )
    chat_id = cursor.lastrowid
    cursor.execute(
        "INSERT INTO participants (chat_id, user_id, role) VALUES (?, ?, 'member')",
        (chat_id, requester_id),
    )
    cursor.execute(
        "INSERT INTO participants (chat_id, user_id, role) VALUES (?, ?, 'member')",
        (chat_id, recipient_id),
    )
    initial_message = (request["message_text"] or "").strip()
    if initial_message:
        cursor.execute(
            """
            INSERT INTO messages (chat_id, sender_id, sender_name, content, timestamp)
            VALUES (?, ?, ?, ?, ?)
            """,
            (
                chat_id,
                requester_id,
                users[requester_id]["display_name"] or users[requester_id]["username"],
                initial_message,
                utc_now_iso(),
            ),
        )
    return chat_id


@router.get("/inbox")
async def get_approval_request_inbox(current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute(
            """
            SELECT ar.id, ar.type, ar.status, ar.message_text, ar.created_at, ar.responded_at,
                   ar.chat_id,
                   requester.id AS requester_id, requester.username AS requester_username,
                   requester.display_name AS requester_display_name,
                   requester.avatar_url AS requester_avatar_url, requester.bio AS requester_bio,
                   requester.created_at AS requester_created_at, requester.last_seen AS requester_last_seen,
                   chats.name AS group_name, chats.description AS group_description,
                   chats.avatar_url AS group_avatar_url
            FROM approval_requests ar
            LEFT JOIN users requester ON requester.id = ar.requester_id
            LEFT JOIN chats ON chats.id = ar.chat_id
            WHERE ar.recipient_id = ?
            ORDER BY CASE ar.status WHEN 'pending' THEN 0 ELSE 1 END, ar.created_at DESC, ar.id DESC
            """,
            (current_user["id"],),
        )
        requests = []
        pending_count = 0
        for row in cursor.fetchall():
            if row["status"] == "pending":
                pending_count += 1
            requester = None
            if row["requester_id"] is not None:
                requester = serialize_user(
                    cursor,
                    {
                        "id": row["requester_id"],
                        "username": row["requester_username"],
                        "display_name": row["requester_display_name"],
                        "avatar_url": row["requester_avatar_url"],
                        "bio": row["requester_bio"],
                        "created_at": row["requester_created_at"],
                        "last_seen": row["requester_last_seen"],
                    },
                    current_user["id"],
                )
            group = None
            if row["chat_id"] is not None:
                group = {
                    "chat_id": row["chat_id"],
                    "name": row["group_name"],
                    "description": row["group_description"] or "",
                    "avatar_url": row["group_avatar_url"] or "/static/avatars/group.png",
                }
            requests.append(
                {
                    "id": row["id"],
                    "type": row["type"],
                    "status": row["status"],
                    "message_text": row["message_text"],
                    "created_at": to_utc_iso(row["created_at"]),
                    "responded_at": to_utc_iso(row["responded_at"]),
                    "requester": requester,
                    "group": group,
                }
            )
        return {"requests": requests, "unread_count": pending_count}
    finally:
        conn.close()


@router.post("/{request_id}/approve")
async def approve_approval_request(request_id: int, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        request = _load_request(cursor, request_id, current_user["id"])
        chat_id = None
        if request["type"] == "direct_message":
            chat_id = _create_direct_chat(cursor, request)
        elif request["type"] == "group_invite":
            cursor.execute("SELECT id FROM chats WHERE id = ? AND type = 'group'", (request["chat_id"],))
            if not cursor.fetchone():
                raise HTTPException(status_code=404, detail="The group no longer exists")
            cursor.execute(
                """
                INSERT OR IGNORE INTO participants (chat_id, user_id, role)
                VALUES (?, ?, 'member')
                """,
                (request["chat_id"], current_user["id"]),
            )
            chat_id = request["chat_id"]
        else:
            raise HTTPException(status_code=400, detail="Unsupported approval request")
        cursor.execute(
            "UPDATE approval_requests SET status = 'approved', responded_at = ? WHERE id = ?",
            (utc_now_iso(), request_id),
        )
        conn.commit()
        return {"message": "Approval request approved", "chat_id": chat_id}
    except HTTPException:
        conn.rollback()
        raise
    except Exception:
        conn.rollback()
        raise
    finally:
        conn.close()


@router.post("/{request_id}/reject")
async def reject_approval_request(request_id: int, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        _load_request(cursor, request_id, current_user["id"])
        cursor.execute(
            "UPDATE approval_requests SET status = 'rejected', responded_at = ? WHERE id = ?",
            (utc_now_iso(), request_id),
        )
        conn.commit()
        return {"message": "Approval request rejected"}
    except HTTPException:
        conn.rollback()
        raise
    except Exception:
        conn.rollback()
        raise
    finally:
        conn.close()
