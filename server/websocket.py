from fastapi import APIRouter, WebSocket, WebSocketDisconnect
from server.connection_manager import ConnectionManager
from server.database import get_connection

router = APIRouter()

manager = ConnectionManager()

@router.websocket("/ws/{username}")
async def websocket_endpoint(websocket: WebSocket, username: str):
    await manager.connect(username, websocket)
    conn = get_connection()
    cursor = conn.cursor()

    try:
        while True:
            # Ожидаем сообщение
            data = await websocket.receive_text()

            # Ожидаем формат "chat_id:сообщение"
            if ":" not in data:
                await websocket.send_text("Ошибка: неверный формат сообщения")
                continue

            chat_id, message = data.split(":", 1)

            # Устанавливаем текущий активный чат для пользователя
            manager.set_user_chat(username, int(chat_id))

            # Проверяем, состоит ли пользователь в этом чате
            cursor.execute("""
                SELECT id
                FROM chats
                WHERE id = ? AND (user1_id = (SELECT id FROM users WHERE username = ?)
                                OR user2_id = (SELECT id FROM users WHERE username = ?))
            """, (chat_id, username, username))
            chat_exists = cursor.fetchone()

            if not chat_exists:
                await websocket.send_text("Ошибка: вы не состоите в этом чате")
                continue

            # Сохраняем сообщение в базу
            cursor.execute("""
                INSERT INTO messages (chat_id, sender_id, content)
                SELECT ?, u.id, ?
                FROM users u
                WHERE u.username = ?
            """, (chat_id, message, username))
            conn.commit()

            # Получаем участников чата
            cursor.execute("""
                SELECT u.username
                FROM chats c
                JOIN users u ON c.user1_id = u.id OR c.user2_id = u.id
                WHERE c.id = ?
            """, (chat_id,))
            participants = [row["username"] for row in cursor.fetchall()]

            # Отправляем сообщение только участникам чата
            for participant in participants:
                if manager.get_user_chat(participant) == int(chat_id):
                    await manager.send_personal_message(f"{username}: {message}", participant)
    except WebSocketDisconnect:
        manager.disconnect(username)
        await manager.broadcast(f"{username} покинул чат.")
    finally:
        conn.close()
