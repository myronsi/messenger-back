import json
from fastapi import APIRouter, HTTPException, Depends, status, UploadFile, File, Form
from pydantic import BaseModel
from server.database import get_connection
from server.routes.auth import get_current_user
from server.websocket import manager
from datetime import datetime
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

@router.post("/upload")
async def upload_file(
    chat_id: int = Form(...),
    file: UploadFile = File(...),
    current_user: dict = Depends(get_current_user)
):
    MAX_FILE_SIZE = 10 * 1024 * 1024
    ALLOWED_FILE_TYPES = {
        "image": [".jpg", ".jpeg", ".png", ".gif"],
        "video": [".mp4", ".mov"],
        "document": [".pdf", ".doc", ".docx", ".txt"]
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

        upload_dir = Path("static/uploads")
        upload_dir.mkdir(parents=True, exist_ok=True)
        unique_filename = f"{uuid.uuid4()}_{file.filename}"
        file_path = upload_dir / unique_filename
        with file_path.open("wb") as buffer:
            buffer.write(content)

        file_url = f"/static/uploads/{unique_filename}"
        file_name = file.filename

        cursor.execute("""
            INSERT INTO messages (chat_id, sender_id, sender_name, content, timestamp)
            VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
        """, (chat_id, current_user["id"], current_user["username"], json.dumps({
            "file_url": file_url,
            "file_name": file_name,
            "file_type": file_type,
            "file_size": file_size
        })))
        message_id = cursor.lastrowid
        conn.commit()

        cursor.execute("SELECT avatar_url FROM users WHERE id = ?", (current_user["id"],))
        user_data = cursor.fetchone()
        avatar_url = user_data["avatar_url"] if user_data and user_data["avatar_url"] else "/static/avatars/default.jpg"

        file_message = {
            "type": "file",
            "username": current_user["username"],
            "avatar_url": avatar_url,
            "is_deleted": False,
            "data": {
                "chat_id": chat_id,
                "file_url": file_url,
                "file_name": file_name,
                "file_type": file_type,
                "file_size": file_size,
                "message_id": message_id,
                "reply_to": None
            },
            "timestamp": datetime.utcnow().isoformat()
        }
        await manager.broadcast(chat_id, file_message)
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

@router.get("/history/{chat_id}")
def get_message_history(chat_id: int, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        cursor.execute("SELECT * FROM participants WHERE chat_id = ? AND user_id = ?", (chat_id, current_user["id"]))
        if not cursor.fetchone():
            logger.error(f"User {current_user['id']} is not a member of chat {chat_id}")
            raise HTTPException(status_code=403, detail="You are not a member of this chat")

        cursor.execute("""
            SELECT messages.id, messages.content, messages.timestamp, messages.sender_name AS sender,
                   messages.reply_to, users.avatar_url
            FROM messages
            LEFT JOIN users ON messages.sender_id = users.id
            WHERE messages.chat_id = ?
            ORDER BY messages.timestamp ASC
        """, (chat_id,))
        messages = cursor.fetchall()
        logger.info(f"Fetched {len(messages)} messages for chat {chat_id}")

        history = []
        for msg in messages:
            try:
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
                        parsed_content = content  # Оставляем как строку, если JSON невалидный
                        message_type = "message"

                history.append({
                    "id": msg["id"],
                    "content": parsed_content,
                    "timestamp": msg["timestamp"],
                    "sender": msg["sender"],
                    "avatar_url": msg["avatar_url"] if msg["avatar_url"] else "/static/avatars/default.jpg",
                    "reply_to": msg["reply_to"],
                    "is_deleted": not bool(msg["content"]),
                    "type": message_type
                })
            except Exception as e:
                logger.error(f"Error processing message {msg['id']} in chat {chat_id}: {str(e)}")
                continue  # Пропускаем проблемное сообщение

        logger.info(f"Returning {len(history)} messages for chat {chat_id}")
        return {"history": history}
    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"Error loading history for chat {chat_id}: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error loading history: {str(e)}")
    finally:
        conn.close()

@router.put("/edit/{message_id}")
def edit_message(message_id: int, payload: MessageEdit, current_user: dict = Depends(get_current_user)):
    content = payload.content.strip()
    if not content:
        raise HTTPException(status_code=400, detail="Message text is required")

    conn = get_connection()
    cursor = conn.cursor()

    try:
        cursor.execute("SELECT sender_id, content FROM messages WHERE id = ?", (message_id,))
        message = cursor.fetchone()
        if not message:
            raise HTTPException(status_code=404, detail="Message not found")
        if message["sender_id"] != current_user["id"]:
            raise HTTPException(status_code=403, detail="You are not the author of this message")
        
        try:
            content_data = json.loads(message["content"])
            if "file_url" in content_data:
                raise HTTPException(status_code=400, detail="File messages cannot be edited")
        except json.JSONDecodeError:
            pass

        cursor.execute("""
            UPDATE messages
            SET content = ?, edited_at = CURRENT_TIMESTAMP
            WHERE id = ?
        """, (content, message_id))
        conn.commit()
        return {"message": "Message successfully updated"}
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error editing message {message_id}: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Internal server error: {str(e)}")
    finally:
        conn.close()

@router.delete("/delete/{message_id}")
def delete_message(message_id: int, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        cursor.execute("SELECT sender_id FROM messages WHERE id = ?", (message_id,))
        message = cursor.fetchone()
        if not message:
            raise HTTPException(status_code=404, detail="Message not found")
        if message["sender_id"] != current_user["id"]:
            raise HTTPException(status_code=403, detail="You are not the author of this message")

        cursor.execute("DELETE FROM messages WHERE id = ?", (message_id,))
        conn.commit()
        return {"message": "Message deleted"}
    except Exception as e:
        conn.rollback()
        logger.error(f"Error deleting message {message_id}: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Internal server error: {str(e)}")
    finally:
        conn.close()