from fastapi import APIRouter, WebSocket, WebSocketDisconnect
from server.connection_manager import ConnectionManager
from server.database import get_connection

router = APIRouter()

manager = ConnectionManager()

@router.websocket("/ws/{username}")
async def websocket_endpoint(websocket: WebSocket, username: str):
    await manager.connect(username, websocket)
    conn = get_connection()  # Подключаемся к базе данных
    cursor = conn.cursor()
    try:
        while True:
            # Ожидаем получение данных
            data = await websocket.receive_text()

            # Пример сообщения: "to:user2 Привет!"
            if data.startswith("to:"):
                target_username, message = data[3:].split(" ", 1)
                cursor.execute("""
                    INSERT INTO messages (sender_id, receiver_id, content)
                    SELECT u1.id, u2.id, ?
                    FROM users u1, users u2
                    WHERE u1.username = ? AND u2.username = ?
                """, (message, username, target_username))
                conn.commit()
                await manager.send_personal_message(f"{username} (лично): {message}", target_username)
            else:
                # Широковещательное сообщение
                await manager.broadcast(f"{username}: {data}")
    except WebSocketDisconnect:
        manager.disconnect(username)
        await manager.broadcast(f"{username} покинул чат.")
    finally:
        conn.close()
