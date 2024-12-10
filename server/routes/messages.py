from fastapi import APIRouter, HTTPException, Depends
from pydantic import BaseModel
from server.database import get_connection

router = APIRouter()

# Модель данных для сообщения
class Message(BaseModel):
    sender: str
    receiver: str
    content: str

class MessageEdit(BaseModel):
    content: str    

# Отправка сообщения
@router.post("/send")
def send_message(message: Message):
    print(f"Получено сообщение: {message.dict()}")
    conn = get_connection()
    cursor = conn.cursor()

    # Получаем ID отправителя и получателя
    cursor.execute("SELECT id FROM users WHERE username = ?", (message.sender,))
    sender_id = cursor.fetchone()
    cursor.execute("SELECT id FROM users WHERE username = ?", (message.receiver,))
    receiver_id = cursor.fetchone()

    if not sender_id or not receiver_id:
        raise HTTPException(status_code=404, detail="Отправитель или получатель не найден")

    # Сохраняем сообщение в базе данных
    cursor.execute("""
        INSERT INTO messages (sender_id, receiver_id, content)
        VALUES (?, ?, ?)
    """, (sender_id["id"], receiver_id["id"], message.content))

    conn.commit()
    conn.close()
    return {"message": "Сообщение отправлено"}

# Получение истории сообщений
@router.get("/history/{user1}/{user2}")
def get_message_history(user1: str, user2: str):
    conn = get_connection()
    cursor = conn.cursor()

    # Получаем ID пользователей
    cursor.execute("SELECT id FROM users WHERE username = ?", (user1,))
    user1_id = cursor.fetchone()
    cursor.execute("SELECT id FROM users WHERE username = ?", (user2,))
    user2_id = cursor.fetchone()

    if not user1_id or not user2_id:
        raise HTTPException(status_code=404, detail="Один из пользователей не найден")

    # Получаем историю сообщений
    cursor.execute("""
        SELECT content, timestamp,
               CASE WHEN sender_id = ? THEN 'sent' ELSE 'received' END as direction
        FROM messages
        WHERE (sender_id = ? AND receiver_id = ?)
           OR (sender_id = ? AND receiver_id = ?)
        ORDER BY timestamp ASC
    """, (user1_id["id"], user1_id["id"], user2_id["id"], user2_id["id"], user1_id["id"]))

    messages = cursor.fetchall()
    conn.close()
    return {"history": [{"content": msg["content"], "timestamp": msg["timestamp"], "direction": msg["direction"]} for msg in messages]}

@router.get("/list/{username}")
def list_chats(username: str):
    conn = get_connection()
    cursor = conn.cursor()

    cursor.execute("SELECT id FROM users WHERE username = ?", (username,))
    user_id = cursor.fetchone()

    if not user_id:
        raise HTTPException(status_code=404, detail="Пользователь не найден")

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

    # Получаем историю сообщений с ID
    cursor.execute("""
        SELECT messages.id, messages.content, messages.timestamp, users.username AS sender
        FROM messages
        JOIN users ON messages.sender_id = users.id
        WHERE messages.chat_id = ?
        ORDER BY messages.timestamp ASC
    """, (chat_id,))
    messages = cursor.fetchall()
    conn.close()

    # Возвращаем данные
    return {
        "history": [
            {
                "id": msg["id"],  # Добавляем ID сообщения
                "content": msg["content"],
                "timestamp": msg["timestamp"],
                "sender": msg["sender"]
            }
            for msg in messages
        ]
    }

@router.put("/edit/{message_id}")
def edit_message(message_id: int, payload: dict):
    content = payload.get("content")
    if not content:
        raise HTTPException(status_code=400, detail="Текст сообщения обязателен")

    conn = get_connection()
    cursor = conn.cursor()

    # Проверяем, существует ли сообщение
    cursor.execute("SELECT * FROM messages WHERE id = ?", (message_id,))
    message = cursor.fetchone()  # Возвращает None, если сообщение не найдено
    if not message:
        raise HTTPException(status_code=404, detail="Сообщение не найдено")

    # Обновляем сообщение
    cursor.execute("""
        UPDATE messages
        SET content = ?, edited_at = CURRENT_TIMESTAMP
        WHERE id = ?
    """, (content, message_id))
    conn.commit()
    conn.close()

    return {"message": "Сообщение успешно обновлено"}


@router.delete("/delete/{message_id}")
def delete_message(message_id: int):
    conn = get_connection()
    cursor = conn.cursor()

    # Проверяем, существует ли сообщение
    cursor.execute("SELECT * FROM messages WHERE id = ?", (message_id,))
    message = cursor.fetchone()
    if not message:
        raise HTTPException(status_code=404, detail="Сообщение не найдено")

    # Удаляем сообщение
    cursor.execute("DELETE FROM messages WHERE id = ?", (message_id,))
    conn.commit()
    conn.close()

    return {"message": "Сообщение удалено"}
