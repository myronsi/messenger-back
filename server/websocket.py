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
            data = await websocket.receive_text()
            print(f"Получено сообщение от {username}: {data}")

            if ":" not in data:
                await websocket.send_text("Ошибка: неверный формат сообщения")
                continue

            chat_id, message = data.split(":", 1)

            # Получаем ID отправителя
            cursor.execute("SELECT id FROM users WHERE username = ?", (username,))
            sender = cursor.fetchone()
            if not sender:
                raise ValueError("Отправитель не найден")
            sender_id = sender["id"]

            # Получаем ID и имя получателя
            cursor.execute("""
                SELECT u.id, u.username
                FROM users u
                JOIN chats c ON (c.user1_id = u.id OR c.user2_id = u.id)
                WHERE c.id = ? AND u.id != ?
            """, (chat_id, sender_id))
            receiver = cursor.fetchone()
            if not receiver:
                raise ValueError("Получатель не найден")
            receiver_id = receiver["id"]

            # Сохраняем сообщение в базу данных
            cursor.execute("""
                INSERT INTO messages (chat_id, sender_id, receiver_id, content)
                VALUES (?, ?, ?, ?)
            """, (chat_id, sender_id, receiver_id, message))
            conn.commit()
            print(f"Сообщение сохранено: {message}")

            # Рассылка сообщения всем участникам чата
            participants = [username, receiver["username"]]
            for participant in participants:
                if participant in manager.active_connections:
                    await manager.send_personal_message(f"{username}: {message}", participant)

    except WebSocketDisconnect:
        print(f"WebSocket отключён: {username}")
        manager.disconnect(username)
    finally:
        conn.close()

