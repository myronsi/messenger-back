import json
from fastapi import APIRouter, HTTPException, Depends, status, UploadFile, File, Form, Query
from pydantic import BaseModel
from server.database import get_connection
from server.routes.auth import get_current_user
from server.websocket import manager
from server.time_utils import to_utc_iso, utc_now_iso
from server.chat_summary import get_message_summary
from server.privacy import DEFAULT_AVATAR, can_send_to_chat, read_receipts_enabled, serialize_user_snapshot
from pathlib import Path
import uuid
import logging

router = APIRouter()

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

class Message(BaseModel):
    chat_id: int
    content: str

class MessageEdit(BaseModel):
    content: str    

def _parse_json_list(value):
    if not value:
        return []
    if isinstance(value, list):
        return value
    try:
        parsed = json.loads(value)
    except (json.JSONDecodeError, TypeError):
        return []
    return parsed if isinstance(parsed, list) else []

def _hydrate_user_items(cursor, items, requester_id: int, respect_read_receipts: bool = False):
    user_ids = sorted({item.get("user_id") for item in items if isinstance(item, dict) and item.get("user_id")})
    if not user_ids:
        return [item for item in items if isinstance(item, dict)]

    placeholders = ",".join(["?"] * len(user_ids))
    cursor.execute(
        f"SELECT id, username, display_name, avatar_url FROM users WHERE id IN ({placeholders})",
        user_ids,
    )
    users = {row["id"]: row for row in cursor.fetchall()}

    hydrated = []
    for item in items:
        if not isinstance(item, dict):
            continue
        user = users.get(item.get("user_id"))
        if respect_read_receipts and user and not read_receipts_enabled(cursor, user["id"]):
            continue
        snapshot = serialize_user_snapshot(cursor, item.get("user_id"), requester_id)
        hydrated.append({
            **item,
            "username": item.get("username") or snapshot["username"],
            "display_name": item.get("display_name") or snapshot["display_name"],
            "avatar_url": snapshot["avatar_url"],
        })
    return hydrated


def _parse_undelivered_to(value):
    if not value:
        return []
    try:
        parsed = json.loads(value)
    except (json.JSONDecodeError, TypeError):
        return []
    return parsed if isinstance(parsed, list) else []


def _message_visible_to(message, user_id: int) -> bool:
    if message["sender_id"] == user_id:
        return True
    return user_id not in _parse_undelivered_to(message["undelivered_to"] if "undelivered_to" in message.keys() else None)


def _undelivered_recipients_for_send(cursor, chat_id: int, sender_id: int) -> tuple[list[int], str | None]:
    if can_send_to_chat(cursor, chat_id, sender_id):
        return [], None

    cursor.execute("SELECT type, user1_id, user2_id FROM chats WHERE id = ?", (chat_id,))
    chat = cursor.fetchone()
    if not chat or chat["type"] != "one-on-one":
        return [], "This message could not be delivered."

    other_id = chat["user2_id"] if chat["user1_id"] == sender_id else chat["user1_id"]
    if not other_id:
        return [], "This message could not be delivered."
    return [other_id], "This message could not be delivered due to the recipient's privacy settings."

@router.post("/upload")
async def upload_file(
    chat_id: int = Form(...),
    file: UploadFile = File(...),
    current_user: dict = Depends(get_current_user)
):
    MAX_FILE_SIZE = 10 * 1024 * 1024
    ALLOWED_FILE_TYPES = {
        "image": [".jpg", ".jpeg", ".png", ".gif"],
        "video": [".mp4", ".mov", ".ogg"],
        "document": [".pdf", ".doc", ".docx", ".txt"],
        "presention": [".pptx"],
        "arcive": [".zip"],
        "audio": [".mp3", ".wav", ".ogg"],
        "code": [".js", ".ts", ".py", ".java", ".cpp", ".html", ".css"],
        "none": [""]
    }

    file_size = 0
    content = await file.read()
    file_size = len(content)
    if file_size > MAX_FILE_SIZE:
        raise HTTPException(status_code=400, detail="File size exceeds 10 MB limit")

    file_extension = Path(file.filename).suffix.lower()
    logger.info(f"File extension: {file_extension}")
    file_type = None
    for type_, extensions in ALLOWED_FILE_TYPES.items():
        if file_extension in extensions:
            file_type = type_
            break
    if not file_type:
        raise HTTPException(status_code=400, detail="Unsupported file type")

    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("SELECT * FROM participants WHERE chat_id = ? AND user_id = ?", (chat_id, current_user["id"]))
        if not cursor.fetchone():
            raise HTTPException(status_code=403, detail="You are not a member of this chat")
        undelivered_to, delivery_error = _undelivered_recipients_for_send(cursor, chat_id, current_user["id"])

        upload_dir = Path("static/uploads")
        upload_dir.mkdir(parents=True, exist_ok=True)
        unique_filename = f"{uuid.uuid4()}_{file.filename}"
        file_path = upload_dir / unique_filename
        with file_path.open("wb") as buffer:
            buffer.write(content)

        file_url = f"/static/uploads/{unique_filename}"
        file_name = file.filename

        cursor.execute("""
            INSERT INTO messages (chat_id, sender_id, sender_name, content, timestamp, delivery_error, undelivered_to)
            VALUES (?, ?, ?, ?, ?, ?, ?)
        """, (chat_id, current_user["id"], current_user["display_name"], json.dumps({
            "file_url": file_url,
            "file_name": file_name,
            "file_type": file_type,
            "file_size": file_size
        }), utc_now_iso(), delivery_error, json.dumps(undelivered_to)))
        message_id = cursor.lastrowid
        conn.commit()

        timestamp = utc_now_iso()

        await manager.broadcast_personalized(chat_id, lambda recipient_id: None if recipient_id in undelivered_to else {
            "type": "file",
            "username": serialize_user_snapshot(cursor, current_user["id"], recipient_id)["display_name"],
            "sender_username": current_user["username"],
            "sender_id": current_user["id"],
            "avatar_url": serialize_user_snapshot(cursor, current_user["id"], recipient_id)["avatar_url"],
            "is_deleted": False,
            "delivery_error": delivery_error if recipient_id == current_user["id"] else None,
            "reactions": [],
            "read_by": [],
            "data": {
                "chat_id": chat_id,
                "file_url": file_url,
                "file_name": file_name,
                "file_type": file_type,
                "file_size": file_size,
                "message_id": message_id,
                "reply_to": None
            },
            "timestamp": timestamp,
        })
        if undelivered_to:
            await manager.send_to_user(current_user["id"], {
                "type": "chat_list_message",
                "chat_id": chat_id,
                "sender_id": current_user["id"],
                "last_message": get_message_summary(cursor, message_id, current_user["id"]),
            })
        else:
            await manager.broadcast(0, {
                "type": "chat_list_message",
                "chat_id": chat_id,
                "sender_id": current_user["id"],
                "last_message": get_message_summary(cursor, message_id),
            })
        logger.info(f"File uploaded and broadcasted: {file_name} to chat {chat_id}")

        return {"message": "File uploaded successfully", "file_url": file_url}
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error uploading file: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error uploading file: {str(e)}")
    finally:
        conn.close()

@router.post("/vm")
async def upload_voice_message(
    chat_id: int = Form(...),
    file: UploadFile = File(...),
    current_user: dict = Depends(get_current_user)
):
    MAX_FILE_SIZE = 10 * 1024 * 1024  # 10 MB
    ALLOWED_FILE_TYPES = [".opus"]

    file_extension = Path(file.filename).suffix.lower()
    if file_extension not in ALLOWED_FILE_TYPES:
        raise HTTPException(status_code=400, detail="Only Opus files are allowed for voice messages")

    content = await file.read()
    file_size = len(content)
    if file_size > MAX_FILE_SIZE:
        raise HTTPException(status_code=400, detail="File size exceeds 10 MB limit")

    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("SELECT * FROM participants WHERE chat_id = ? AND user_id = ?", (chat_id, current_user["id"]))
        if not cursor.fetchone():
            raise HTTPException(status_code=403, detail="You are not a member of this chat")
        undelivered_to, delivery_error = _undelivered_recipients_for_send(cursor, chat_id, current_user["id"])

        upload_dir = Path("static/vm")
        upload_dir.mkdir(parents=True, exist_ok=True)
        unique_filename = f"{uuid.uuid4()}_{file.filename}"
        file_path = upload_dir / unique_filename
        with file_path.open("wb") as buffer:
            buffer.write(content)

        file_url = f"/static/vm/{unique_filename}"
        file_name = file.filename
        file_type = "voice"

        cursor.execute("""
            INSERT INTO messages (chat_id, sender_id, sender_name, content, timestamp, delivery_error, undelivered_to)
            VALUES (?, ?, ?, ?, ?, ?, ?)
        """, (chat_id, current_user["id"], current_user["display_name"], json.dumps({
            "file_url": file_url,
            "file_name": file_name,
            "file_type": file_type,
            "file_size": file_size
        }), utc_now_iso(), delivery_error, json.dumps(undelivered_to)))
        message_id = cursor.lastrowid
        conn.commit()

        timestamp = utc_now_iso()

        await manager.broadcast_personalized(chat_id, lambda recipient_id: None if recipient_id in undelivered_to else {
            "type": "file",
            "username": serialize_user_snapshot(cursor, current_user["id"], recipient_id)["display_name"],
            "sender_username": current_user["username"],
            "sender_id": current_user["id"],
            "avatar_url": serialize_user_snapshot(cursor, current_user["id"], recipient_id)["avatar_url"],
            "is_deleted": False,
            "delivery_error": delivery_error if recipient_id == current_user["id"] else None,
            "reactions": [],
            "read_by": [],
            "data": {
                "chat_id": chat_id,
                "file_url": file_url,
                "file_name": file_name,
                "file_type": file_type,
                "file_size": file_size,
                "message_id": message_id,
                "reply_to": None
            },
            "timestamp": timestamp,
        })
        if undelivered_to:
            await manager.send_to_user(current_user["id"], {
                "type": "chat_list_message",
                "chat_id": chat_id,
                "sender_id": current_user["id"],
                "last_message": get_message_summary(cursor, message_id, current_user["id"]),
            })
        else:
            await manager.broadcast(0, {
                "type": "chat_list_message",
                "chat_id": chat_id,
                "sender_id": current_user["id"],
                "last_message": get_message_summary(cursor, message_id),
            })
        logger.info(f"Voice message uploaded and broadcasted: {file_name} to chat {chat_id}")

        return {"message": "Voice message uploaded successfully", "file_url": file_url}
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error uploading voice message: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error uploading voice message: {str(e)}")
    finally:
        conn.close()

@router.get("/history/{chat_id}")
async def get_message_history(
    chat_id: int,
    limit: int = Query(50, ge=1, le=100),
    before_id: int | None = Query(None, ge=1),
    current_user: dict = Depends(get_current_user)
):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        cursor.execute("SELECT * FROM participants WHERE chat_id = ? AND user_id = ?", (chat_id, current_user["id"]))
        if not cursor.fetchone():
            logger.error(f"User {current_user['id']} is not a member of chat {chat_id}")
            raise HTTPException(status_code=403, detail="You are not a member of this chat")

        history_limit = limit + 1
        if before_id:
            cursor.execute("""
                SELECT messages.id, messages.sender_id, messages.content, messages.timestamp,
                       COALESCE(users.display_name, messages.sender_name) AS sender,
                       users.username AS sender_username,
                       messages.reply_to, messages.reactions, users.avatar_url, messages.read_by,
                       messages.edited_at, messages.delivery_error, messages.undelivered_to
                FROM messages
                LEFT JOIN users ON messages.sender_id = users.id
                WHERE messages.chat_id = ? AND messages.id < ?
                ORDER BY messages.id DESC
                LIMIT ?
            """, (chat_id, before_id, history_limit))
        else:
            cursor.execute("""
                SELECT messages.id, messages.sender_id, messages.content, messages.timestamp,
                       COALESCE(users.display_name, messages.sender_name) AS sender,
                       users.username AS sender_username,
                       messages.reply_to, messages.reactions, users.avatar_url, messages.read_by,
                       messages.edited_at, messages.delivery_error, messages.undelivered_to
                FROM messages
                LEFT JOIN users ON messages.sender_id = users.id
                WHERE messages.chat_id = ?
                ORDER BY messages.id DESC
                LIMIT ?
            """, (chat_id, history_limit))
        messages = cursor.fetchall()
        has_more = len(messages) > limit
        messages = list(reversed(messages[:limit]))
        logger.info(f"Fetched {len(messages)} messages for chat {chat_id}")

        history = []
        for msg in messages:
            try:
                if not _message_visible_to(msg, current_user["id"]):
                    continue
                content = msg["content"]
                message_type = "message"
                parsed_content = content
                if content and content.startswith("{"):
                    try:
                        parsed_content = json.loads(content)
                        if isinstance(parsed_content, dict) and "file_url" in parsed_content:
                            message_type = "file"
                    except json.JSONDecodeError as json_err:
                        logger.error(f"Failed to parse JSON content for message {msg['id']}: {content}, error: {json_err}")
                        parsed_content = content  # Keep as string if JSON is invalid
                        message_type = "message"

                sender_snapshot = serialize_user_snapshot(cursor, msg["sender_id"], current_user["id"])
                history.append({
                    "id": msg["id"],
                    "sender_id": msg["sender_id"],
                    "content": parsed_content,
                    "timestamp": to_utc_iso(msg["timestamp"]),
                    "sender": sender_snapshot["display_name"] or msg["sender"],
                    "sender_username": msg["sender_username"],
                    "avatar_url": sender_snapshot["avatar_url"],
                    "reply_to": msg["reply_to"],
                    "edited_at": to_utc_iso(msg["edited_at"]) if msg["edited_at"] else None,
                    "reactions": _hydrate_user_items(cursor, _parse_json_list(msg["reactions"]), current_user["id"]),
                    "read_by": _hydrate_user_items(cursor, _parse_json_list(msg["read_by"]), current_user["id"], respect_read_receipts=True),
                    "is_deleted": not bool(msg["content"]),
                    "type": message_type,
                    "delivery_error": msg["delivery_error"]
                })
            except Exception as e:
                logger.error(f"Error processing message {msg['id']} in chat {chat_id}: {str(e)}")
                continue  # Skip problematic message

        next_before_id = history[0]["id"] if has_more and history else None
        logger.info(f"Returning {len(history)} messages for chat {chat_id}")
        return {
            "history": history,
            "has_more": has_more,
            "next_before_id": next_before_id
        }
    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"Error loading history for chat {chat_id}: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error loading history: {str(e)}")
    finally:
        conn.close()    

# @router.put("/edit/{message_id}")
# def edit_message(message_id: int, payload: MessageEdit, current_user: dict = Depends(get_current_user)):
#     content = payload.content.strip()
#     if not content:
#         raise HTTPException(status_code=400, detail="Message text is required")

#     conn = get_connection()
#     cursor = conn.cursor()

#     try:
#         cursor.execute("SELECT sender_id, content FROM messages WHERE id = ?", (message_id,))
#         message = cursor.fetchone()
#         if not message:
#             raise HTTPException(status_code=404, detail="Message not found")
#         if message["sender_id"] != current_user["id"]:
#             raise HTTPException(status_code=403, detail="You are not the author of this message")
        
#         try:
#             content_data = json.loads(message["content"])
#             if "file_url" in content_data:
#                 raise HTTPException(status_code=400, detail="File messages cannot be edited")
#         except json.JSONDecodeError:
#             pass

#         cursor.execute("""
#             UPDATE messages
#             SET content = ?, edited_at = CURRENT_TIMESTAMP
#             WHERE id = ?
#         """, (content, message_id))
#         conn.commit()
#         return {"message": "Message successfully updated"}
#     except HTTPException:
#         raise
#     except Exception as e:
#         conn.rollback()
#         logger.error(f"Error editing message {message_id}: {str(e)}")
#         raise HTTPException(status_code=500, detail=f"Internal server error: {str(e)}")
#     finally:
#         conn.close()

# @router.delete("/delete/{message_id}")
# def delete_message(message_id: int, current_user: dict = Depends(get_current_user)):
#     conn = get_connection()
#     cursor = conn.cursor()

#     try:
#         cursor.execute("SELECT sender_id FROM messages WHERE id = ?", (message_id,))
#         message = cursor.fetchone()
#         if not message:
#             raise HTTPException(status_code=404, detail="Message not found")
#         if message["sender_id"] != current_user["id"]:
#             raise HTTPException(status_code=403, detail="You are not the author of this message")

#         cursor.execute("DELETE FROM messages WHERE id = ?", (message_id,))
#         conn.commit()
#         return {"message": "Message deleted"}
#     except Exception as e:
#         conn.rollback()
#         logger.error(f"Error deleting message {message_id}: {str(e)}")
#         raise HTTPException(status_code=500, detail=f"Internal server error: {str(e)}")
#     finally:
#         conn.close()
