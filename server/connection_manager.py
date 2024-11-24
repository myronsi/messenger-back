from typing import List, Dict
from fastapi import WebSocket

class ConnectionManager:
    def __init__(self):
        self.active_connections: Dict[str, WebSocket] = {}  # Карта пользователей к их соединениям

    async def connect(self, username: str, websocket: WebSocket):
        await websocket.accept()
        self.active_connections[username] = websocket

    def disconnect(self, username: str):
        if username in self.active_connections:
            del self.active_connections[username]

    async def send_personal_message(self, message: str, username: str):
        # Отправка личного сообщения, если пользователь онлайн
        websocket = self.active_connections.get(username)
        if websocket:
            await websocket.send_text(message)

    async def broadcast(self, message: str):
        # Широковещательная рассылка всем пользователям
        for connection in self.active_connections.values():
            await connection.send_text(message)
