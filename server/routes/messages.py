from fastapi import APIRouter, HTTPException, Depends
from pydantic import BaseModel
from server.database import get_connection

router = APIRouter()

# Data model for message
class Message(BaseModel):
    sender: str
    receiver: str
    content: str

class MessageEdit(BaseModel):
    content: str    

# Sending a message
@router.post("/send")
def send_message(message: Message):
    print(f"Message received: {message.dict()}")
    conn = get_connection()
    cursor = conn.cursor()

    # Get the sender and recipient IDs
    cursor.execute("SELECT id FROM users WHERE username = ?", (message.sender,))
    sender_id = cursor.fetchone()
    cursor.execute("SELECT id FROM users WHERE username = ?", (message.receiver,))
    receiver_id = cursor.fetchone()

    if not sender_id or not receiver_id:
        raise HTTPException(status_code=404, detail="Sender or recipient not found")

    # Save the message in the database
        cursor.execute("""
        INSERT INTO messages (sender_id, receiver_id, content)
        VALUES (?, ?, ?)
    """, (sender_id["id"], receiver_id["id"], message.content))

    conn.commit()
    conn.close()
    return {"message": "Message sent"}

# Getting message history
@router.get("/history/{user1}/{user2}")
def get_message_history(user1: str, user2: str):
    conn = get_connection()
    cursor = conn.cursor()

    # Get user IDs
    cursor.execute("SELECT id FROM users WHERE username = ?", (user1,))
    user1_id = cursor.fetchone()
    cursor.execute("SELECT id FROM users WHERE username = ?", (user2,))
    user2_id = cursor.fetchone()

    if not user1_id or not user2_id:
        raise HTTPException(status_code=404, detail="One of the users was not found")

    # Get message history
        cursor.execute("""
        SELECT content, timestamp,
               CASE WHEN sender_id = ? THEN 'sent' ELSE 'received' END as direction
        FROM messages
        WHERE (sender_id = ? AND receiver_id = ?)
           OR (sender_id = ? AND receiver_id = ?)
        ORDER BY timestamp ASC
    """, (user1_id["id"], user1_id["id"], user2_id["id"], user2_id["id"], user1_id["id"]))

    messages = cursor.fetchall()
    conn.close()
    return {"history": [{"content": msg["content"], "timestamp": msg["timestamp"], "direction": msg["direction"]} for msg in messages]}

@router.get("/list/{username}")
def list_chats(username: str):
    conn = get_connection()
    cursor = conn.cursor()

    cursor.execute("SELECT id FROM users WHERE username = ?", (username,))
    user_id = cursor.fetchone()

    if not user_id:
        raise HTTPException(status_code=404, detail="User not found")

    cursor.execute("""
        SELECT id, name FROM chats
        WHERE user1_id = ? OR user2_id = ?
    """, (user_id["id"], user_id["id"]))
    chats = cursor.fetchall()
    conn.close()

    return {"chats": [{"id": chat["id"], "name": chat["name"]} for chat in chats]}

@router.get("/history/{chat_id}")
def get_message_history(chat_id: int):
    conn = get_connection()
    cursor = conn.cursor()

    # Get message history with ID
    cursor.execute("""
        SELECT messages.id, messages.content, messages.timestamp, users.username AS sender
        FROM messages
        JOIN users ON messages.sender_id = users.id
        WHERE messages.chat_id = ?
        ORDER BY messages.timestamp ASC
    """, (chat_id,))
    messages = cursor.fetchall()
    conn.close()

    # Returning data
    return {
        "history": [
            {
                "id": msg["id"],  # Add message ID
                "content": msg["content"],
                "timestamp": msg["timestamp"],
                "sender": msg["sender"]
            }
            for msg in messages
        ]
    }

@router.put("/edit/{message_id}")
def edit_message(message_id: int, payload: dict):
    content = payload.get("content")
    if not content:
        raise HTTPException(status_code=400, detail="Message text is required")

    conn = get_connection()
    cursor = conn.cursor()

    # Check if the message exists
    cursor.execute("SELECT * FROM messages WHERE id = ?", (message_id,))
    message = cursor.fetchone()  # Returns None if message not found
    if not message:
        raise HTTPException(status_code=404, detail="Message not found")

    # Updating the message
        cursor.execute("""
        UPDATE messages
        SET content = ?, edited_at = CURRENT_TIMESTAMP
        WHERE id = ?
    """, (content, message_id))
    conn.commit()
    conn.close()

    return {"message": "Message successfully updated"}

@router.delete("/delete/{message_id}")
def delete_message(message_id: int):
    conn = get_connection()
    cursor = conn.cursor()

    # Check if the message exists
    cursor.execute("SELECT * FROM messages WHERE id = ?", (message_id,))
    message = cursor.fetchone()
    if not message:
        raise HTTPException(status_code=404, detail="Message not found")

    # Delete the message
    cursor.execute("DELETE FROM messages WHERE id = ?", (message_id,))
    conn.commit()
    conn.close()

    return {"message": "Message deleted"}
