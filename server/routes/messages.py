import json
from fastapi import APIRouter, HTTPException, Depends, status
from pydantic import BaseModel
from server.database import get_connection
from server.routes.auth import get_current_user
from server.websocket import manager
from datetime import datetime

router = APIRouter()

class Message(BaseModel):
    chat_id: int
    content: str

class MessageEdit(BaseModel):
    content: str    

@router.get("/history/{chat_id}")
def get_message_history(chat_id: int, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        cursor.execute("SELECT * FROM participants WHERE chat_id = ? AND user_id = ?", (chat_id, current_user["id"]))
        if not cursor.fetchone():
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

        return {
            "history": [
                {
                    "id": msg["id"],
                    "content": msg["content"],
                    "timestamp": msg["timestamp"],
                    "sender": msg["sender"],  # Всегда используем sender_name
                    "avatar_url": msg["avatar_url"] if msg["avatar_url"] else "/static/avatars/default.jpg",
                    "reply_to": msg["reply_to"],
                    "is_deleted": not bool(msg["avatar_url"])  # Явно указываем статус удаления
                }
                for msg in messages
            ]
        }
    except Exception as e:
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
        # Проверяем, что сообщение существует и пользователь — автор
        cursor.execute("SELECT sender_id FROM messages WHERE id = ?", (message_id,))
        message = cursor.fetchone()
        if not message:
            raise HTTPException(status_code=404, detail="Message not found")
        if message["sender_id"] != current_user["id"]:
            raise HTTPException(status_code=403, detail="You are not the author of this message")

        cursor.execute("""
            UPDATE messages
            SET content = ?, edited_at = CURRENT_TIMESTAMP
            WHERE id = ?
        """, (content, message_id))
        conn.commit()
        return {"message": "Message successfully updated"}

    except Exception as e:
        conn.rollback()
        raise HTTPException(status_code=500, detail=f"Internal server error: {str(e)}")
    finally:
        conn.close()

@router.delete("/delete/{message_id}")
def delete_message(message_id: int, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        # Проверяем, что сообщение существует и пользователь — автор
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
        raise HTTPException(status_code=500, detail=f"Internal server error: {str(e)}")
    finally:
        conn.close()