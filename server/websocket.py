from fastapi import APIRouter, WebSocket, WebSocketDisconnect, Query
from server.database import get_connection
from server.routes.auth import verify_token
import json
from datetime import datetime
import logging
import sqlite3

router = APIRouter()

# Настройка логирования
logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

class ConnectionManager:
    def __init__(self):
        self.active_chats = {}  # { chat_id: [websockets] }

    async def connect(self, chat_id: int, websocket: WebSocket):
        await websocket.accept()
        if chat_id not in self.active_chats:
            self.active_chats[chat_id] = []
        self.active_chats[chat_id].append(websocket)

    def disconnect(self, chat_id: int, websocket: WebSocket):
        if chat_id in self.active_chats:
            self.active_chats[chat_id].remove(websocket)
            if not self.active_chats[chat_id]:  # Если нет подключённых клиентов, удаляем чат
                del self.active_chats[chat_id]

    async def broadcast(self, chat_id: int, message: dict):
        if chat_id in self.active_chats:
            for websocket in self.active_chats[chat_id]:
                await websocket.send_text(json.dumps(message))

manager = ConnectionManager()

@router.websocket("/ws/chat/{chat_id}")
async def websocket_endpoint(websocket: WebSocket, chat_id: int, token: str = Query(...)):
    # Проверка токена
    user = verify_token(token)
    if not user:
        await websocket.send_text("Неверный токен")
        await websocket.close(code=1008)  # Policy Violation
        return

    username = user["username"]
    user_id = user["id"]

    # Подключение к БД
    conn = get_connection()
    cursor = conn.cursor()

    # Проверка, является ли пользователь участником чата
    cursor.execute("SELECT * FROM participants WHERE chat_id = ? AND user_id = ?", (chat_id, user_id))
    if not cursor.fetchone():
        await websocket.send_text("Вы не участник этого чата")
        await websocket.close(code=1008)
        conn.close()
        return

    # Подключение WebSocket
    await manager.connect(chat_id, websocket)
    logger.info(f"🔗 WebSocket подключён для {username} в чате {chat_id}")

    try:
        while True:
            # Получаем сообщение от клиента
            data = await websocket.receive_text()
            logger.info(f"📩 Получено сообщение в чате {chat_id} от {username}: {data}")

            # Парсим JSON из данных
            try:
                parsed_data = json.loads(data)
                content = parsed_data["content"]
            except (json.JSONDecodeError, KeyError) as e:
                await websocket.send_text("Ошибка: Неверный формат сообщения")
                logger.error(f"Ошибка парсинга JSON: {e}")
                continue

            if not content.strip():
                await websocket.send_text("Ошибка: Пустое сообщение")
                continue

            # Сохраняем сообщение в БД
            try:
                cursor.execute("""
                    INSERT INTO messages (chat_id, sender_id, content, timestamp)
                    VALUES (?, ?, ?, CURRENT_TIMESTAMP)
                """, (chat_id, user_id, content))
                conn.commit()
                message_id = cursor.lastrowid  # Получаем ID последнего вставленного сообщения
                logger.info(f"Сообщение сохранено в БД: {{'chat_id': {chat_id}, 'content': '{content}'}}, ID: {message_id}")
            except sqlite3.Error as e:
                logger.error(f"Ошибка сохранения сообщения в БД: {e}")
                await websocket.send_text("Ошибка: Не удалось сохранить сообщение")
                continue

            # Формируем сообщение для отправки
            message = {
                "username": username,
                "data": {
                    "chat_id": chat_id,
                    "content": content,
                    "message_id": message_id  # Добавляем ID сообщения
                },
                "timestamp": datetime.utcnow().isoformat()
            }

            # Рассылаем сообщение всем участникам чата
            await manager.broadcast(chat_id, message)

    except WebSocketDisconnect:
        logger.info(f"🔴 {username} отключился от чата {chat_id}")
        manager.disconnect(chat_id, websocket)
    finally:
        conn.close()