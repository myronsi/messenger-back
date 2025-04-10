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
def get_message_history(chat_id: int):
    conn = get_connection()
    cursor = conn.cursor()

    cursor.execute("""
        SELECT messages.id, messages.content, messages.timestamp, users.username AS sender, users.avatar_url
        FROM messages
        JOIN users ON messages.sender_id = users.id
        WHERE messages.chat_id = ?
        ORDER BY messages.timestamp ASC
    """, (chat_id,))
    messages = cursor.fetchall()
    conn.close()

    return {
        "history": [
            {
                "id": msg["id"],
                "content": msg["content"],
                "timestamp": msg["timestamp"],
                "sender": msg["sender"],
                "avatar_url": msg["avatar_url"]
            }
            for msg in messages
        ]
    }

@router.put("/edit/{message_id}")
def edit_message(message_id: int, payload: MessageEdit):
    content = payload.content.strip()
    if not content:
        raise HTTPException(status_code=400, detail="Message text is required")

    conn = get_connection()
    cursor = conn.cursor()

    try:
        cursor.execute("SELECT id FROM messages WHERE id = ?", (message_id,))
        message = cursor.fetchone()
        if not message:
            raise HTTPException(status_code=404, detail="Message not found")

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
def delete_message(message_id: int):
    conn = get_connection()
    cursor = conn.cursor()

    cursor.execute("SELECT * FROM messages WHERE id = ?", (message_id,))
    message = cursor.fetchone()
    if not message:
        raise HTTPException(status_code=404, detail="Message not found")

    cursor.execute("DELETE FROM messages WHERE id = ?", (message_id,))
    conn.commit()
    conn.close()

    return {"message": "Message deleted"}
