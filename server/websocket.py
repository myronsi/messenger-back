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
            print(f"Received message from {username}: {data}")

            if ":" not in data:
                await websocket.send_text("Error: Invalid message format")
                continue

            chat_id, message = data.split(":", 1)

            # Get the sender ID
            cursor.execute("SELECT id FROM users WHERE username = ?", (username,))
            sender = cursor.fetchone()
            if not sender:
                raise ValueError("Sender not found")
            sender_id = sender["id"]

            # Get the recipient's ID and name
            cursor.execute("""
                SELECT u.id, u.username
                FROM users u
                JOIN chats c ON (c.user1_id = u.id OR c.user2_id = u.id)
                WHERE c.id = ? AND u.id != ?
            """, (chat_id, sender_id))
            receiver = cursor.fetchone()
            if not receiver:
                raise ValueError("Recipient not found")
            receiver_id = receiver["id"]

            # Save the message to the database
            cursor.execute("""
                INSERT INTO messages (chat_id, sender_id, receiver_id, content)
                VALUES (?, ?, ?, ?)
            """, (chat_id, sender_id, receiver_id, message))
            conn.commit()
            print(f"Message saved: {message}")

            # Sending a message to all chat participants
            participants = [username, receiver["username"]]
            for participant in participants:
                if participant in manager.active_connections:
                    await manager.send_personal_message(f"{username}: {message}", participant)

    except WebSocketDisconnect:
        print(f"WebSocket отключён: {username}")
        manager.disconnect(username)
    finally:
        conn.close()

