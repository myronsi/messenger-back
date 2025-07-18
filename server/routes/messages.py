import json
from fastapi import APIRouter, HTTPException, Depends, status, UploadFile, File, Form
from pydantic import BaseModel
from server.database import get_connection, release_connection
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
        cursor.execute("SELECT * FROM participants WHERE chat_id = %s AND user_id = %s", (chat_id, current_user["id"]))
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
            VALUES (%s, %s, %s, %s, CURRENT_TIMESTAMP)
            RETURNING id
        """, (chat_id, current_user["id"], current_user["username"], json.dumps({
            "file_url": file_url,
            "file_name": file_name,
            "file_type": file_type,
            "file_size": file_size
        })))
        message_id = cursor.fetchone()[0]
        conn.commit()

        cursor.execute("SELECT avatar_url FROM users WHERE id = %s", (current_user["id"],))
        user_data = cursor.fetchone()
        avatar_url = user_data[0] if user_data and user_data[0] else "/static/avatars/default.jpg"

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
        release_connection(conn)

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
        cursor.execute("SELECT * FROM participants WHERE chat_id = %s AND user_id = %s", (chat_id, current_user["id"]))
        if not cursor.fetchone():
            raise HTTPException(status_code=403, detail="You are not a member of this chat")

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
            INSERT INTO messages (chat_id, sender_id, sender_name, content, timestamp)
            VALUES (%s, %s, %s, %s, CURRENT_TIMESTAMP)
            RETURNING id
        """, (chat_id, current_user["id"], current_user["username"], json.dumps({
            "file_url": file_url,
            "file_name": file_name,
            "file_type": file_type,
            "file_size": file_size
        })))
        message_id = cursor.fetchone()[0]
        conn.commit()

        cursor.execute("SELECT avatar_url FROM users WHERE id = %s", (current_user["id"],))
        user_data = cursor.fetchone()
        avatar_url = user_data[0] if user_data and user_data[0] else "/static/avatars/default.jpg"

        voice_message = {
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
        await manager.broadcast(chat_id, voice_message)
        logger.info(f"Voice message uploaded and broadcasted: {file_name} to chat {chat_id}")

        return {"message": "Voice message uploaded successfully", "file_url": file_url}
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error uploading voice message: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error uploading voice message: {str(e)}")
    finally:
        release_connection(conn)

@router.get("/history/{chat_id}")
async def get_message_history(chat_id: int, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        cursor.execute("SELECT * FROM participants WHERE chat_id = %s AND user_id = %s", (chat_id, current_user["id"]))
        if not cursor.fetchone():
            logger.error(f"User {current_user['id']} is not a member of chat {chat_id}")
            raise HTTPException(status_code=403, detail="You are not a member of this chat")

        cursor.execute("""
            SELECT m.id, m.content, m.timestamp, m.sender_name AS sender,
                   m.reply_to, m.reactions, u.avatar_url, m.read_by
            FROM messages m
            LEFT JOIN users u ON m.sender_id = u.id
            WHERE m.chat_id = %s
            ORDER BY m.timestamp ASC
        """, (chat_id,))
        messages = cursor.fetchall()
        logger.info(f"Fetched {len(messages)} messages for chat {chat_id}")

        history = []
        for msg in messages:
            try:
                content = msg[1]
                message_type = "message"
                parsed_content = content
                if content and content.startswith("{"):
                    try:
                        parsed_content = json.loads(content)
                        if isinstance(parsed_content, dict) and "file_url" in parsed_content:
                            message_type = "file"
                    except json.JSONDecodeError as json_err:
                        logger.error(f"Failed to parse JSON content for message {msg[0]}: {content}, error: {json_err}")
                        parsed_content = content
                        message_type = "message"

                history.append({
                    "id": msg[0],
                    "content": parsed_content,
                    "timestamp": msg[2],
                    "sender": msg[3],
                    "avatar_url": msg[6] if msg[6] else "/static/avatars/default.jpg",
                    "reply_to": msg[4],
                    "reactions": msg[5] or "[]",
                    "read_by": msg[7] or "[]",
                    "is_deleted": not bool(msg[1]),
                    "type": message_type
                })
            except Exception as e:
                logger.error(f"Error processing message {msg[0]} in chat {chat_id}: {str(e)}")
                continue

        logger.info(f"Returning {len(history)} messages for chat {chat_id}")
        return {"history": history}
    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"Error loading history for chat {chat_id}: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error loading history: {str(e)}")
    finally:
        release_connection(conn)