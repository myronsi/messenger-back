from fastapi import APIRouter, HTTPException, Depends
from pydantic import BaseModel
from server.database import get_connection

router = APIRouter()

# Модель данных для сообщения
class Message(BaseModel):
    sender: str
    receiver: str
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
