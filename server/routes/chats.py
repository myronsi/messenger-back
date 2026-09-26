from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel
import json
from server.database import get_connection
from server.routes.auth import get_current_user
from server.websocket import manager
from server.presence import is_user_online
from server.time_utils import to_utc_iso, utc_now_iso
from server.chat_summary import get_chat_unread_summary, message_visible_to
from server.privacy import DEFAULT_AVATAR, PERMISSION_APPROVAL_REQUIRED, PERMISSION_DENIED, direct_chat_permission, get_direct_chat_id, is_blocked_between, serialize_user
from server.privacy import read_receipts_enabled, serialize_user_snapshot
from server.routes.requests import create_direct_message_request
import logging

router = APIRouter()

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

class ChatCreate(BaseModel):
    user1: str
    user2: str
    initial_message: str | None = None


class MarkChatReadRequest(BaseModel):
    message_ids: list[int] | None = None
    mark_all: bool = False


def _parse_read_by(read_by_value):
    if not read_by_value:
        return []
    if isinstance(read_by_value, list):
        return read_by_value
    try:
        parsed = json.loads(read_by_value)
    except (json.JSONDecodeError, TypeError):
        return []
    return parsed if isinstance(parsed, list) else []


def _ensure_chat_participant(cursor, chat_id: int, user_id: int):
    cursor.execute("SELECT id, type, user1_id, user2_id FROM chats WHERE id = ?", (chat_id,))
    chat = cursor.fetchone()
    if not chat:
        raise HTTPException(status_code=404, detail="Chat not found")
    cursor.execute("SELECT 1 FROM participants WHERE chat_id = ? AND user_id = ?", (chat_id, user_id))
    if cursor.fetchone():
        return
    if chat["type"] == "one-on-one" and user_id in (chat["user1_id"], chat["user2_id"]):
        return
    raise HTTPException(status_code=403, detail="You are not a member of this chat")


def _is_chat_pinned(cursor, user_id: int, chat_id: int) -> bool:
    cursor.execute("SELECT 1 FROM user_chat_pins WHERE user_id = ? AND chat_id = ?", (user_id, chat_id))
    return cursor.fetchone() is not None

@router.post("/create")
async def create_chat(chat: ChatCreate, current_user: dict = Depends(get_current_user)):
    if chat.user1 != current_user["username"]:
        raise HTTPException(status_code=403, detail="You can only create chats as yourself")

    conn = get_connection()
    cursor = conn.cursor()

    try:
        # Check if users exist
        cursor.execute("SELECT id, username, display_name, avatar_url, bio, last_seen FROM users WHERE username = ?", (chat.user1,))
        user1 = cursor.fetchone()
        cursor.execute("SELECT id, username, display_name, avatar_url, bio, last_seen FROM users WHERE username = ?", (chat.user2,))
        user2 = cursor.fetchone()

        if not user1 or not user2:
            raise HTTPException(status_code=404, detail="One or both users not found")
        if chat.user1 == chat.user2:
            raise HTTPException(status_code=400, detail="Cannot create chat with yourself")

        # Check if chat already exists
        existing_chat_id = get_direct_chat_id(cursor, user1["id"], user2["id"])
        if existing_chat_id:
            if is_blocked_between(cursor, user1["id"], user2["id"]):
                raise HTTPException(status_code=403, detail="This user does not allow direct messages from you")
            return {
                "chat_id": existing_chat_id,
                "message": "Chat already exists"
            }

        permission = direct_chat_permission(cursor, user1["id"], user2["id"])
        if permission == PERMISSION_APPROVAL_REQUIRED:
            request_id, already_pending = create_direct_message_request(cursor, user1["id"], user2["id"], chat.initial_message or "")
            conn.commit()
            if not already_pending:
                await manager.send_to_user(user1["id"], {
                    "type": "approval_request_created",
                    "request_id": request_id,
                    "request_type": "direct_message",
                })
                await manager.send_to_user(user2["id"], {
                    "type": "approval_request_created",
                    "request_id": request_id,
                    "request_type": "direct_message",
                })
            return {
                "approval_required": True,
                "already_pending": already_pending,
                "request_id": request_id,
                "message": "Already waiting for user approval" if already_pending else "Waiting for user approval"
            }
        if permission == PERMISSION_DENIED:
            raise HTTPException(status_code=403, detail="This user does not allow direct messages from you")

        # Create chat
        chat_name = f"{chat.user1} & {chat.user2}"
        cursor.execute("""
            INSERT INTO chats (name, type, user1_id, user2_id)
            VALUES (?, 'one-on-one', ?, ?)
        """, (chat_name, user1["id"], user2["id"]))
        chat_id = cursor.lastrowid

        # Add participants
        cursor.execute("INSERT INTO participants (chat_id, user_id) VALUES (?, ?)", (chat_id, user1["id"]))
        logger.info(f"Added participant: chat_id={chat_id}, user_id={user1['id']}")
        cursor.execute("INSERT INTO participants (chat_id, user_id) VALUES (?, ?)", (chat_id, user2["id"]))
        logger.info(f"Added participant: chat_id={chat_id}, user_id={user2['id']}")

        conn.commit()

    # Prepare WebSocket notification
        chat_data = {
            "type": "chat_created",
            "chat": {
                "chat_id": chat_id,
                "name": chat_name,
                "user1": chat.user1,
                "user2": chat.user2,
                "user1_avatar_url": DEFAULT_AVATAR,
                "user2_avatar_url": DEFAULT_AVATAR
            }
        }
        await manager.broadcast(0, chat_data)
        logger.info(f"Sent chat_created notification for chat_id={chat_id} to chat_id=0")

        # If an initial message was provided, save it and broadcast to the chat
        initial_msg_id = None
        if chat.initial_message:
            try:
                cursor.execute("""
                    INSERT INTO messages (chat_id, sender_id, sender_name, content, timestamp)
                    VALUES (?, ?, ?, ?, ?)
                """, (chat_id, user1["id"], user1["display_name"] or chat.user1, chat.initial_message, utc_now_iso()))
                conn.commit()
                initial_msg_id = cursor.lastrowid
                # Fetch avatar
                cursor.execute("SELECT avatar_url FROM users WHERE id = ?", (user1["id"],))
                user_data = cursor.fetchone()
                avatar_url = user_data["avatar_url"] if user_data and user_data["avatar_url"] else "/static/avatars/default.jpg"

                message = {
                    "type": "message",
                    "username": user1["display_name"] or chat.user1,
                    "sender_id": user1["id"],
                    "avatar_url": avatar_url,
                    "is_deleted": False,
                    "data": {
                        "chat_id": chat_id,
                        "content": chat.initial_message,
                        "message_id": initial_msg_id,
                        "reply_to": None
                    },
                    "timestamp": utc_now_iso()
                }
                await manager.broadcast(chat_id, message)
                logger.info(f"Saved and broadcast initial message id={initial_msg_id} for chat {chat_id}")
            except Exception as e:
                conn.rollback()
                logger.error(f"Failed to save initial message for chat {chat_id}: {e}")

        return {
            "chat_id": chat_id,
            "message": "Chat created successfully"
        }
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error creating chat: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error creating chat: {str(e)}")
    finally:
        conn.close()

@router.get("/list/{username}")
async def list_chats(username: str, current_user: dict = Depends(get_current_user)):
    if username != current_user["username"]:
        raise HTTPException(status_code=403, detail="You can only view your own chats")

    conn = get_connection()
    cursor = conn.cursor()

    try:
        cursor.execute("SELECT id FROM users WHERE username = ?", (username,))
        user_id = cursor.fetchone()
        if not user_id:
            raise HTTPException(status_code=404, detail="User not found")

        cursor.execute("""
            SELECT c.id, c.name, c.user1_id, c.user2_id,
                   u1.username AS user1_username, u1.display_name AS user1_display_name,
                   u1.avatar_url AS user1_avatar_url, u1.bio AS user1_bio, u1.last_seen AS user1_last_seen,
                   u2.username AS user2_username, u2.display_name AS user2_display_name,
                   u2.avatar_url AS user2_avatar_url, u2.bio AS user2_bio, u2.last_seen AS user2_last_seen
            FROM chats c
            JOIN participants p ON c.id = p.chat_id
            LEFT JOIN users u1 ON c.user1_id = u1.id
            LEFT JOIN users u2 ON c.user2_id = u2.id
            WHERE p.user_id = ? AND c.type = 'one-on-one'
        """, (user_id["id"],))
        chats = cursor.fetchall()

        chat_list = []
        for chat in chats:
            is_user1 = chat["user1_id"] == user_id["id"]
            interlocutor_id = chat["user2_id"] if is_user1 else chat["user1_id"]
            interlocutor_username = chat["user2_username"] if is_user1 else chat["user1_username"]
            interlocutor_display_name = chat["user2_display_name"] if is_user1 else chat["user1_display_name"]
            interlocutor_deleted = not interlocutor_username
            if interlocutor_deleted:
                interlocutor = None
            else:
                interlocutor = {
                    "id": interlocutor_id,
                    "username": interlocutor_username,
                    "display_name": interlocutor_display_name,
                    "avatar_url": chat["user2_avatar_url"] if is_user1 else chat["user1_avatar_url"],
                    "bio": chat["user2_bio"] if is_user1 else chat["user1_bio"],
                    "last_seen": chat["user2_last_seen"] if is_user1 else chat["user1_last_seen"],
                }
                interlocutor = serialize_user(cursor, interlocutor, user_id["id"])

            chat_list.append({
                "id": chat["id"],
                "name": interlocutor_username or "Deleted User",
                "interlocutor_name": interlocutor_username or "Deleted User",
                "interlocutor_display_name": interlocutor["display_name"] if interlocutor else "Deleted User",
                "avatar_url": interlocutor["avatar_url"] if interlocutor else DEFAULT_AVATAR,
                "interlocutor_is_online": interlocutor["is_online"] if interlocutor else False,
                "interlocutor_last_seen": interlocutor["last_seen"] if interlocutor else None,
                "interlocutor_deleted": interlocutor_deleted,
                "is_pinned": _is_chat_pinned(cursor, user_id["id"], chat["id"]),
                **get_chat_unread_summary(cursor, chat["id"], user_id["id"]),
            })

        cursor.execute(
            """
            SELECT ar.id, ar.message_text, ar.created_at,
                   u.id AS recipient_id, u.username, u.display_name, u.avatar_url, u.bio, u.last_seen
            FROM approval_requests ar
            JOIN users u ON u.id = ar.recipient_id
            WHERE ar.type = 'direct_message'
              AND ar.requester_id = ?
              AND ar.status = 'pending'
              AND NOT EXISTS (
                SELECT 1
                FROM chats c
                WHERE c.type = 'one-on-one'
                  AND (
                    (c.user1_id = ar.requester_id AND c.user2_id = ar.recipient_id)
                    OR (c.user1_id = ar.recipient_id AND c.user2_id = ar.requester_id)
                  )
              )
            """,
            (user_id["id"],),
        )
        for request in cursor.fetchall():
            recipient = serialize_user(cursor, {
                "id": request["recipient_id"],
                "username": request["username"],
                "display_name": request["display_name"],
                "avatar_url": request["avatar_url"],
                "bio": request["bio"],
                "last_seen": request["last_seen"],
            }, user_id["id"])
            chat_list.append({
                "id": -request["id"],
                "name": request["username"],
                "interlocutor_name": request["username"],
                "interlocutor_display_name": recipient["display_name"],
                "avatar_url": recipient["avatar_url"],
                "interlocutor_is_online": recipient["is_online"],
                "interlocutor_last_seen": recipient["last_seen"],
                "interlocutor_deleted": False,
                "is_pinned": False,
                "pending_approval_request": True,
                "pending_request_id": request["id"],
                "last_message": {
                    "id": -request["id"],
                    "sender_id": user_id["id"],
                    "sender_name": current_user["display_name"] or current_user["username"],
                    "content": request["message_text"] or "",
                    "type": "message",
                    "timestamp": to_utc_iso(request["created_at"]),
                    "read_by": [],
                    "delivery_error": "Waiting for user approval",
                },
                "unread_count": 0,
                "first_unread_message_id": None,
            })

        return {"chats": chat_list}
    except Exception as e:
        logger.error(f"Error fetching chats: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error fetching chats: {str(e)}")
    finally:
        conn.close()


@router.put("/{chat_id}/pin")
async def pin_chat(chat_id: int, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        _ensure_chat_participant(cursor, chat_id, current_user["id"])
        cursor.execute("""
            INSERT INTO user_chat_pins (user_id, chat_id, pinned_at)
            VALUES (?, ?, ?)
            ON CONFLICT(user_id, chat_id)
            DO UPDATE SET pinned_at = excluded.pinned_at
        """, (current_user["id"], chat_id, utc_now_iso()))
        conn.commit()
        return {"chat_id": chat_id, "is_pinned": True}
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error pinning chat {chat_id}: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error pinning chat: {str(e)}")
    finally:
        conn.close()


@router.delete("/{chat_id}/pin")
async def unpin_chat(chat_id: int, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        _ensure_chat_participant(cursor, chat_id, current_user["id"])
        cursor.execute("DELETE FROM user_chat_pins WHERE user_id = ? AND chat_id = ?", (current_user["id"], chat_id))
        conn.commit()
        return {"chat_id": chat_id, "is_pinned": False}
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error unpinning chat {chat_id}: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error unpinning chat: {str(e)}")
    finally:
        conn.close()


@router.post("/{chat_id}/read")
@router.post("/{chat_id}/read/")
async def mark_chat_read(chat_id: int, payload: MarkChatReadRequest | None = None, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        _ensure_chat_participant(cursor, chat_id, current_user["id"])

        payload = payload or MarkChatReadRequest()

        if payload.mark_all:
            cursor.execute("""
                SELECT id, sender_id, read_by, undelivered_to
                FROM messages
                WHERE chat_id = ? AND sender_id != ?
                ORDER BY id ASC
            """, (chat_id, current_user["id"]))
        else:
            message_ids = []
            seen_ids = set()
            for message_id in payload.message_ids or []:
                if message_id > 0 and message_id not in seen_ids:
                    seen_ids.add(message_id)
                    message_ids.append(message_id)

            if not message_ids:
                summary = get_chat_unread_summary(cursor, chat_id, current_user["id"])
                return {
                    "chat_id": chat_id,
                    "unread_count": summary["unread_count"],
                    "first_unread_message_id": summary["first_unread_message_id"],
                    "read_message_ids": [],
                    "read_at": utc_now_iso(),
                }

            placeholders = ",".join(["?"] * len(message_ids))
            cursor.execute(f"""
                SELECT id, sender_id, read_by, undelivered_to
                FROM messages
                WHERE chat_id = ?
                  AND sender_id != ?
                  AND id IN ({placeholders})
                ORDER BY id ASC
            """, [chat_id, current_user["id"], *message_ids])

        timestamp = utc_now_iso()
        receipt_is_public = read_receipts_enabled(cursor, current_user["id"])
        read_message_ids = []

        for message in cursor.fetchall():
            if not message_visible_to(message, current_user["id"]):
                continue

            read_by = _parse_read_by(message["read_by"])
            if any(isinstance(read, dict) and read.get("user_id") == current_user["id"] for read in read_by):
                continue

            read_item = {**serialize_user_snapshot(cursor, current_user["id"], current_user["id"]), "read_at": timestamp}
            if not receipt_is_public:
                read_item["hidden"] = True

            read_by.append(read_item)
            cursor.execute("UPDATE messages SET read_by = ? WHERE id = ?", (json.dumps(read_by), message["id"]))
            read_message_ids.append(message["id"])

        conn.commit()
        summary = get_chat_unread_summary(cursor, chat_id, current_user["id"])
        response = {
            "chat_id": chat_id,
            "unread_count": summary["unread_count"],
            "first_unread_message_id": summary["first_unread_message_id"],
            "read_message_ids": read_message_ids,
            "read_at": timestamp,
        }

        if read_message_ids and receipt_is_public:
            await manager.broadcast_personalized(chat_id, lambda recipient_id: {
                "type": "chat_read_batch",
                "chat_id": chat_id,
                "reader_user_id": current_user["id"],
                "message_ids": read_message_ids,
                **serialize_user_snapshot(cursor, current_user["id"], recipient_id),
                "read_at": timestamp,
                "timestamp": timestamp,
            })

        await manager.send_to_user(current_user["id"], {
            "type": "chat_read_batch",
            "chat_id": chat_id,
            "reader_user_id": current_user["id"],
            "message_ids": read_message_ids,
            "unread_count": summary["unread_count"],
            "first_unread_message_id": summary["first_unread_message_id"],
            "read_at": timestamp,
            "timestamp": timestamp,
        })

        return response
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error marking chat {chat_id} as read: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error marking chat as read: {str(e)}")
    finally:
        conn.close()

@router.delete("/delete/{chat_id}")
async def delete_chat(chat_id: int, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        # Check if chat exists and user is a participant
        cursor.execute("SELECT user1_id, user2_id FROM chats WHERE id = ? AND type = 'one-on-one'", (chat_id,))
        chat = cursor.fetchone()
        if not chat:
            raise HTTPException(status_code=404, detail="Chat not found")
        if current_user["id"] not in (chat["user1_id"], chat["user2_id"]):
            raise HTTPException(status_code=403, detail="You are not a member of this chat")

        # Delete related data
        cursor.execute("DELETE FROM participants WHERE chat_id = ?", (chat_id,))
        cursor.execute("DELETE FROM messages WHERE chat_id = ?", (chat_id,))
        cursor.execute("DELETE FROM chats WHERE id = ?", (chat_id,))

        conn.commit()

        # Notify via WebSocket
        message = {
            "type": "chat_deleted",
            "chat_id": chat_id
        }
        await manager.broadcast(0, message)
        logger.info(f"Sent chat_deleted notification for chat_id={chat_id} to chat_id=0")

        return {"message": "Chat deleted successfully"}
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error deleting chat: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error deleting chat: {str(e)}")
    finally:
        conn.close()
