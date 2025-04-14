import json
from fastapi import APIRouter, WebSocket, WebSocketDisconnect, Query
from server.database import get_connection
from server.routes.auth import verify_token
from datetime import datetime
import logging
import sqlite3

router = APIRouter()

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

class ConnectionManager:
    def __init__(self):
        self.active_chats = {}  # { chat_id: [websockets] }

    async def connect(self, chat_id: int, websocket: WebSocket):
        # Удаляем await websocket.accept(), так как соединение уже принято
        if chat_id not in self.active_chats:
            self.active_chats[chat_id] = []
        self.active_chats[chat_id].append(websocket)
        logger.info(f"Connected to chat {chat_id}. Active connections: {len(self.active_chats[chat_id])}")

    def disconnect(self, chat_id: int, websocket: WebSocket):
        if chat_id in self.active_chats:
            self.active_chats[chat_id].remove(websocket)
            if not self.active_chats[chat_id]:
                del self.active_chats[chat_id]
            logger.info(f"Disconnected from chat {chat_id}. Active connections: {len(self.active_chats.get(chat_id, []))}")

    async def broadcast(self, chat_id: int, message: dict):
        if chat_id in self.active_chats:
            logger.info(f"Broadcasting to chat {chat_id}: {message}, clients: {len(self.active_chats[chat_id])}")
            for websocket in self.active_chats[chat_id]:
                try:
                    await websocket.send_text(json.dumps(message))
                    logger.info(f"Sent message to client in chat {chat_id}")
                except Exception as e:
                    logger.error(f"Error broadcasting to chat {chat_id}: {e}")

    async def broadcast_to_chat(self, chat_id: int, message: dict):
        if chat_id in self.active_chats:
            logger.info(f"Broadcasting to chat {chat_id}: {message}")
            for websocket in self.active_chats[chat_id]:
                try:
                    await websocket.send_text(json.dumps(message))
                except Exception as e:
                    logger.error(f"Error broadcasting to chat {chat_id}: {e}")

manager = ConnectionManager()

@router.websocket("/ws/chat/{chat_id}")
async def websocket_endpoint(websocket: WebSocket, chat_id: int, token: str = Query(...)):
    # Принимаем соединение WebSocket
    await websocket.accept()

    # Проверка токена
    user = verify_token(token)
    if not user:
        await websocket.send_text(json.dumps({"type": "error", "message": "Invalid token"}))
        await websocket.close(code=1008)
        return

    username = user["username"]
    user_id = user["id"]

    # Подключение к базе данных
    conn = get_connection()
    cursor = conn.cursor()

    # Проверка существования пользователя
    cursor.execute("SELECT id FROM users WHERE id = ?", (user_id,))
    if not cursor.fetchone():
        await websocket.send_text(json.dumps({"type": "error", "message": "Account does not exist"}))
        await websocket.close(code=1008)
        conn.close()
        return

    # Пропускаем проверку участия для chat_id=0 (глобальные уведомления)
    if chat_id != 0:
        cursor.execute("SELECT * FROM participants WHERE chat_id = ? AND user_id = ?", (chat_id, user_id))
        participant = cursor.fetchone()
        if not participant:
            logger.error(f"User {user_id} not found in participants for chat {chat_id}")
            await websocket.send_text(json.dumps({"type": "error", "message": "You are not a member of this chat"}))
            await websocket.close(code=1008)
            conn.close()
            return
        logger.info(f"User {user_id} verified as participant in chat {chat_id}")

    # Получаем avatar_url пользователя
    cursor.execute("SELECT avatar_url FROM users WHERE id = ?", (user_id,))
    user_data = cursor.fetchone()
    avatar_url = user_data["avatar_url"] if user_data and user_data["avatar_url"] else "/static/avatars/default.jpg"

    # Подключение клиента к WebSocket
    await manager.connect(chat_id, websocket)
    logger.info(f"WebSocket CONNECTED for {username} in chat {chat_id}")

    try:
        while True:
            data = await websocket.receive_text()
            logger.info(f"Received message in chat {chat_id} from {username}: {data}")

            try:
                parsed_data = json.loads(data)
                message_type = parsed_data.get("type", "message")
                content = parsed_data.get("content")
                message_id = parsed_data.get("message_id")
                reply_to = parsed_data.get("reply_to")
            except (json.JSONDecodeError, KeyError) as e:
                await websocket.send_text(json.dumps({"type": "error", "message": "Invalid message format"}))
                logger.error(f"JSON parsing error: {e}")
                continue

            if message_type == "message":
                if not content or not content.strip():
                    await websocket.send_text(json.dumps({"type": "error", "message": "Empty message"}))
                    continue

                try:
                    cursor.execute("""
                        INSERT INTO messages (chat_id, sender_id, sender_name, content, timestamp, reply_to)
                        VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP, ?)
                    """, (chat_id, user_id, username, content, reply_to))
                    conn.commit()
                    message_id = cursor.lastrowid
                    logger.info(f"Message saved in db: {{'chat_id': {chat_id}, 'sender_name': '{username}', 'content': '{content}', 'reply_to': {reply_to}}}, ID: {message_id}")
                except sqlite3.Error as e:
                    logger.error(f"Error while saving message to db: {e}")
                    await websocket.send_text(json.dumps({"type": "error", "message": "Failed to save message"}))
                    continue

                message = {
                    "type": "message",
                    "username": username,
                    "avatar_url": avatar_url,
                    "is_deleted": False,
                    "data": {
                        "chat_id": chat_id,
                        "content": content,
                        "message_id": message_id,
                        "reply_to": reply_to
                    },
                    "timestamp": datetime.utcnow().isoformat()
                }
                await manager.broadcast(chat_id, message)

            elif message_type == "edit":
                if not message_id or not content:
                    await websocket.send_text(json.dumps({"type": "error", "message": "Missing message_id or content"}))
                    continue

                try:
                    cursor.execute("SELECT sender_id FROM messages WHERE id = ?", (message_id,))
                    sender_id = cursor.fetchone()
                    if not sender_id or sender_id[0] != user_id:
                        await websocket.send_text(json.dumps({"type": "error", "message": "You are not the author of this message"}))
                        continue

                    cursor.execute("UPDATE messages SET content = ? WHERE id = ?", (content, message_id))
                    conn.commit()
                    logger.info(f"Message edited: {{'message_id': {message_id}, 'new_content': '{content}'}}")
                except sqlite3.Error as e:
                    logger.error(f"Error while editing message: {e}")
                    await websocket.send_text(json.dumps({"type": "error", "message": "Failed to edit message"}))
                    continue

                edit_message = {
                    "type": "edit",
                    "message_id": message_id,
                    "new_content": content,
                    "timestamp": datetime.utcnow().isoformat()
                }
                await manager.broadcast(chat_id, edit_message)

            elif message_type == "delete":
                if not message_id:
                    await websocket.send_text(json.dumps({"type": "error", "message": "Missing message_id"}))
                    continue

                try:
                    cursor.execute("SELECT sender_id FROM messages WHERE id = ?", (message_id,))
                    sender_id = cursor.fetchone()
                    if not sender_id or sender_id[0] != user_id:
                        await websocket.send_text(json.dumps({"type": "error", "message": "You are not the author of this message"}))
                        continue

                    cursor.execute("DELETE FROM messages WHERE id = ?", (message_id,))
                    conn.commit()
                    logger.info(f"Message deleted: {{'message_id': {message_id}}}")
                except sqlite3.Error as e:
                    logger.error(f"Error while deleting message: {e}")
                    await websocket.send_text(json.dumps({"type": "error", "message": "Failed to delete message"}))
                    continue

                delete_message = {
                    "type": "delete",
                    "message_id": message_id,
                    "timestamp": datetime.utcnow().isoformat()
                }
                await manager.broadcast(chat_id, delete_message)

    except WebSocketDisconnect:
        logger.info(f"{username} DISCONNECTED from {chat_id}")
        manager.disconnect(chat_id, websocket)
    finally:
        conn.close()