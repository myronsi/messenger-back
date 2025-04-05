import json
from fastapi import APIRouter, HTTPException, Depends, status
from pydantic import BaseModel
from server.database import get_connection
from server.routes.auth import get_current_user
from server.websocket import manager
from datetime import datetime

router = APIRouter()

# Data model for message
class Message(BaseModel):
    chat_id: int
    content: str

class MessageEdit(BaseModel):
    content: str    

# Sending a message
@router.post("/send")
def send_message(message: Message, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()

    # Проверяем, существует ли чат
    cursor.execute("SELECT id FROM chats WHERE id = ?", (message.chat_id,))
    chat = cursor.fetchone()
    if not chat:
        raise HTTPException(status_code=404, detail="Чат не найден")

    # Сохраняем сообщение
    print(f"Получено сообщение для сохранения: chat_id={message.chat_id}, sender_id={current_user['id']}, content={message.content}")
    cursor.execute("""
        INSERT INTO messages (chat_id, sender_id, content, timestamp)
        VALUES (?, ?, ?, CURRENT_TIMESTAMP)
    """, (message.chat_id, current_user["id"], message.content))
    conn.commit()
    print("Сообщение успешно сохранено через REST API")
    conn.close()

    return {"message": "Сообщение успешно отправлено"}


@router.get("/list/{username}")
def list_chats(username: str):
    conn = get_connection()
    cursor = conn.cursor()

    cursor.execute("SELECT id FROM users WHERE username = ?", (username,))
    user_id = cursor.fetchone()

    if not user_id:
        raise HTTPException(status_code=404, detail="User not found")

    cursor.execute("""
        SELECT id, name FROM chats
        WHERE user1_id = ? OR user2_id = ?
    """, (user_id["id"], user_id["id"]))
    chats = cursor.fetchall()
    conn.close()

    return {"chats": [{"id": chat["id"], "name": chat["name"]} for chat in chats]}

@router.get("/history/{chat_id}")
def get_message_history(chat_id: int):
    conn = get_connection()
    cursor = conn.cursor()

    # Get message history with ID
    cursor.execute("""
        SELECT messages.id, messages.content, messages.timestamp, users.username AS sender
        FROM messages
        JOIN users ON messages.sender_id = users.id
        WHERE messages.chat_id = ?
        ORDER BY messages.timestamp ASC
    """, (chat_id,))
    messages = cursor.fetchall()
    conn.close()

    # Returning data
    return {
        "history": [
            {
                "id": msg["id"],  # Add message ID
                "content": msg["content"],
                "timestamp": msg["timestamp"],
                "sender": msg["sender"]
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
        # Проверяем, существует ли сообщение
        cursor.execute("SELECT id FROM messages WHERE id = ?", (message_id,))
        message = cursor.fetchone()
        if not message:
            raise HTTPException(status_code=404, detail="Message not found")

        # Обновляем сообщение
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

    # Check if the message exists
    cursor.execute("SELECT * FROM messages WHERE id = ?", (message_id,))
    message = cursor.fetchone()
    if not message:
        raise HTTPException(status_code=404, detail="Message not found")

    # Delete the message
    cursor.execute("DELETE FROM messages WHERE id = ?", (message_id,))
    conn.commit()
    conn.close()

    return {"message": "Message deleted"}
