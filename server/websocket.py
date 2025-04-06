from fastapi import APIRouter, WebSocket, WebSocketDisconnect, Query
from server.database import get_connection
from server.routes.auth import verify_token
import json
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
        await websocket.accept()
        if chat_id not in self.active_chats:
            self.active_chats[chat_id] = []
        self.active_chats[chat_id].append(websocket)

    def disconnect(self, chat_id: int, websocket: WebSocket):
        if chat_id in self.active_chats:
            self.active_chats[chat_id].remove(websocket)
            if not self.active_chats[chat_id]:
                del self.active_chats[chat_id]

    async def broadcast(self, chat_id: int, message: dict):
        if chat_id in self.active_chats:
            for websocket in self.active_chats[chat_id]:
                await websocket.send_text(json.dumps(message))

manager = ConnectionManager()

@router.websocket("/ws/chat/{chat_id}")
async def websocket_endpoint(websocket: WebSocket, chat_id: int, token: str = Query(...)):
    user = verify_token(token)
    if not user:
        await websocket.send_text("Invalid token")
        await websocket.close(code=1008)
        return

    username = user["username"]
    user_id = user["id"]

    conn = get_connection()
    cursor = conn.cursor()

    cursor.execute("SELECT * FROM participants WHERE chat_id = ? AND user_id = ?", (chat_id, user_id))
    if not cursor.fetchone():
        await websocket.send_text("You are not the member of the chat")
        await websocket.close(code=1008)
        conn.close()
        return

    await manager.connect(chat_id, websocket)
    logger.info(f"WebSocket CONNECTED for {username} in chat {chat_id}")

    try:
        while True:
            data = await websocket.receive_text()
            logger.info(f"Recieved message in chat {chat_id} from {username}: {data}")

            try:
                parsed_data = json.loads(data)
                content = parsed_data["content"]
            except (json.JSONDecodeError, KeyError) as e:
                await websocket.send_text("Error: Invalid message format")
                logger.error(f"JSON parsing error: {e}")
                continue

            if not content.strip():
                await websocket.send_text("Error: Empty message")
                continue

            try:
                cursor.execute("""
                    INSERT INTO messages (chat_id, sender_id, content, timestamp)
                    VALUES (?, ?, ?, CURRENT_TIMESTAMP)
                """, (chat_id, user_id, content))
                conn.commit()
                message_id = cursor.lastrowid
                logger.info(f"Message saved in db: {{'chat_id': {chat_id}, 'content': '{content}'}}, ID: {message_id}")
            except sqlite3.Error as e:
                logger.error(f"Error while saving message to db: {e}")
                await websocket.send_text("Error: Failed to save message")
                continue

            message = {
                "username": username,
                "data": {
                    "chat_id": chat_id,
                    "content": content,
                    "message_id": message_id
                },
                "timestamp": datetime.utcnow().isoformat()
            }

            await manager.broadcast(chat_id, message)

    except WebSocketDisconnect:
        logger.info(f"{username} DISCONNECTED from {chat_id}")
        manager.disconnect(chat_id, websocket)
    finally:
        conn.close()