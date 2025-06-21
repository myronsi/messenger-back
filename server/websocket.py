from fastapi import WebSocket, WebSocketDisconnect, Query
from fastapi.routing import APIRouter
from server.database import get_connection
from server.routes.auth import verify_token
from datetime import datetime
import logging
import sqlite3
import json

router = APIRouter()

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

class ConnectionManager:
    def __init__(self):
        self.active_chats = {}  # { chat_id: [websockets] }

    async def connect(self, chat_id: int, websocket: WebSocket):
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
    # Проверка токена
    user = verify_token(token)
    if not user:
        await websocket.accept()
        await websocket.send_text(json.dumps({"type": "error", "message": "Invalid token"}))
        await websocket.close(code=1008)
        return

    username = user["username"]
    user_id = user["id"]

    # Подключение к базе данных
    conn = get_connection()
    cursor = conn.cursor()

    try:
        # Проверка существования пользователя
        cursor.execute("SELECT id FROM users WHERE id = ?", (user_id,))
        if not cursor.fetchone():
            await websocket.accept()
            await websocket.send_text(json.dumps({"type": "error", "message": "Account does not exist"}))
            await websocket.close(code=1008)
            return

        # Принимаем соединение WebSocket после проверки токена и пользователя
        await websocket.accept()

        # Пропускаем проверку для chat_id=0 (глобальные уведомления)
        if chat_id != 0:
            # Проверка существования чата
            cursor.execute("SELECT id FROM chats WHERE id = ?", (chat_id,))
            if not cursor.fetchone():
                await websocket.send_text(json.dumps({"type": "error", "message": "Chat does not exist"}))
                await websocket.close(code=1008)
                logger.error(f"Chat {chat_id} does not exist")
                return

            # Проверка участия пользователя
            cursor.execute("SELECT * FROM participants WHERE chat_id = ? AND user_id = ?", (chat_id, user_id))
            participant = cursor.fetchone()
            if not participant:
                await websocket.send_text(json.dumps({"type": "error", "message": "You are not a member of this chat"}))
                await websocket.close(code=1008)
                logger.error(f"User {user_id} not found in participants for chat {chat_id}")
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
                    file_url = parsed_data.get("file_url")
                    file_name = parsed_data.get("file_name")
                    file_type = parsed_data.get("file_type")
                    file_size = parsed_data.get("file_size")
                    reaction = parsed_data.get("reaction")
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

                elif message_type == "file":
                    if not file_url or not file_name or not file_type or not file_size:
                        await websocket.send_text(json.dumps({"type": "error", "message": "Missing file metadata"}))
                        continue

                    try:
                        cursor.execute("""
                            INSERT INTO messages (chat_id, sender_id, sender_name, content, timestamp, reply_to)
                            VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP, ?)
                        """, (chat_id, user_id, username, json.dumps({
                            "file_url": file_url,
                            "file_name": file_name,
                            "file_type": file_type,
                            "file_size": file_size
                        }), reply_to))
                        conn.commit()
                        message_id = cursor.lastrowid
                        logger.info(f"File message saved in db: {{'chat_id': {chat_id}, 'sender_name': '{username}', 'file_url': '{file_url}'}}, ID: {message_id}")
                    except sqlite3.Error as e:
                        logger.error(f"Error while saving file message to db: {e}")
                        await websocket.send_text(json.dumps({"type": "error", "message": "Failed to save file message"}))
                        continue

                    file_message = {
                        "type": "file",
                        "username": username,
                        "avatar_url": avatar_url,
                        "is_deleted": False,
                        "data": {
                            "chat_id": chat_id,
                            "file_url": file_url,
                            "file_name": file_name,
                            "file_type": file_type,
                            "file_size": file_size,
                            "message_id": message_id,
                            "reply_to": reply_to
                        },
                        "timestamp": datetime.utcnow().isoformat()
                    }
                    await manager.broadcast(chat_id, file_message)

                elif message_type == "edit":
                    if not message_id or not content:
                        await websocket.send_text(json.dumps({"type": "error", "message": "Missing message_id or content"}))
                        continue

                    try:
                        cursor.execute("SELECT sender_id FROM messages WHERE id = ?", (message_id,))
                        sender_id = cursor.fetchone()
                        if not sender_id or sender_id["sender_id"] != user_id:
                            await websocket.send_text(json.dumps({"type": "error", "message": "You are not the author of this message"}))
                            continue

                        cursor.execute("UPDATE messages SET content = ?, edited_at = CURRENT_TIMESTAMP WHERE id = ?", (content, message_id))
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
                        if not sender_id or sender_id["sender_id"] != user_id:
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

                elif message_type == "reaction_add":
                    if not message_id or not reaction:
                        await websocket.send_text(json.dumps({"type": "error", "message": "Missing message_id or reaction"}))
                        continue

                    try:
                        cursor.execute("SELECT reactions FROM messages WHERE id = ?", (message_id,))
                        result = cursor.fetchone()
                        if not result:
                            await websocket.send_text(json.dumps({"type": "error", "message": "Message not found"}))
                            continue

                        reactions = json.loads(result["reactions"])
                        if any(r["user_id"] == user_id and r["reaction"] == reaction for r in reactions):
                            await websocket.send_text(json.dumps({"type": "error", "message": "You already reacted with this reaction"}))
                            continue

                        reactions.append({"user_id": user_id, "reaction": reaction})
                        cursor.execute("UPDATE messages SET reactions = ? WHERE id = ?", (json.dumps(reactions), message_id))
                        conn.commit()
                        logger.info(f"Reaction added: {{'message_id': {message_id}, 'user_id': {user_id}, 'reaction': '{reaction}'}}")
                    except sqlite3.Error as e:
                        logger.error(f"Error while adding reaction: {e}")
                        await websocket.send_text(json.dumps({"type": "error", "message": "Failed to add reaction"}))
                        continue

                    reaction_message = {
                        "type": "reaction_add",
                        "message_id": message_id,
                        "user_id": user_id,
                        "reaction": reaction,
                        "timestamp": datetime.utcnow().isoformat()
                    }
                    await manager.broadcast(chat_id, reaction_message)

                elif message_type == "reaction_remove":
                    if not message_id or not reaction:
                        await websocket.send_text(json.dumps({"type": "error", "message": "Missing message_id or reaction"}))
                        continue

                    try:
                        cursor.execute("SELECT reactions FROM messages WHERE id = ?", (message_id,))
                        result = cursor.fetchone()
                        if not result:
                            await websocket.send_text(json.dumps({"type": "error", "message": "Message not found"}))
                            continue

                        reactions = json.loads(result["reactions"])
                        if not any(r["user_id"] == user_id and r["reaction"] == reaction for r in reactions):
                            await websocket.send_text(json.dumps({"type": "error", "message": "You cannot remove this reaction"}))
                            continue

                        new_reactions = [r for r in reactions if not (r["user_id"] == user_id and r["reaction"] == reaction)]
                        cursor.execute("UPDATE messages SET reactions = ? WHERE id = ?", (json.dumps(new_reactions), message_id))
                        conn.commit()
                        logger.info(f"Reaction removed: {{'message_id': {message_id}, 'user_id': {user_id}, 'reaction': '{reaction}'}}")
                    except sqlite3.Error as e:
                        logger.error(f"Error while removing reaction: {e}")
                        await websocket.send_text(json.dumps({"type": "error", "message": "Failed to remove reaction"}))
                        continue

                    reaction_message = {
                        "type": "reaction_remove",
                        "message_id": message_id,
                        "user_id": user_id,
                        "reaction": reaction,
                        "timestamp": datetime.utcnow().isoformat()
                    }
                    await manager.broadcast(chat_id, reaction_message)

                elif message_type == "group_created":
                    if chat_id == 0:
                        logger.info(f"Received group_created for chat {parsed_data.get('chat_id')}")
                        await manager.broadcast(chat_id, parsed_data)

        except WebSocketDisconnect:
            logger.info(f"{username} DISCONNECTED from {chat_id}")
            manager.disconnect(chat_id, websocket)
        except Exception as e:
            logger.error(f"Unexpected error in WebSocket for {username} in chat {chat_id}: {e}")
            manager.disconnect(chat_id, websocket)
            await websocket.close(code=1000)
    finally:
        conn.close()