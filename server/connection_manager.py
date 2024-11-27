from typing import List, Dict
from fastapi import WebSocket

class ConnectionManager:
    def __init__(self):
        self.active_connections: dict[str, WebSocket] = {}
        self.user_chats: dict[str, int] = {}  # Отслеживание активных чатов пользователей

    async def connect(self, username: str, websocket: WebSocket):
        await websocket.accept()
        self.active_connections[username] = websocket

    def disconnect(self, username: str):
        if username in self.active_connections:
            del self.active_connections[username]
        if username in self.user_chats:
            del self.user_chats[username]

    async def send_personal_message(self, message: str, username: str):
        if username in self.active_connections:
            await self.active_connections[username].send_text(message)

    async def broadcast(self, message: str):
        for connection in self.active_connections.values():
            await connection.send_text(message)

    def set_user_chat(self, username: str, chat_id: int):
        """Установить активный чат для пользователя."""
        self.user_chats[username] = chat_id

    def get_user_chat(self, username: str) -> int:
        """Получить текущий активный чат пользователя."""
        return self.user_chats.get(username)
