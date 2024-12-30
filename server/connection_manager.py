from typing import List, Dict
from fastapi import WebSocket

class ConnectionManager:
    def __init__(self):
        self.active_connections: dict[str, WebSocket] = {}

    async def connect(self, username: str, websocket: WebSocket):
        await websocket.accept()
        self.active_connections[username] = websocket
        print(f"{username} connected")

    def disconnect(self, username: str):
        if username in self.active_connections:
            del self.active_connections[username]
            print(f"{username} disabled")

    async def send_personal_message(self, message: str, username: str):
        if username in self.active_connections:
            websocket = self.active_connections[username]
            await websocket.send_text(message)
            print(f"Message sent {username}: {message}")

    async def broadcast(self, message: str):
        for username, websocket in self.active_connections.items():
            await websocket.send_text(message)
            print(f"Message sent {username}: {message}")

    def set_user_chat(self, username: str, chat_id: int):
        """Set active chat for the user."""
        self.user_chats[username] = chat_id

    def get_user_chat(self, username: str) -> int:
        """Get the user's currently active chat."""
        return self.user_chats.get(username)
