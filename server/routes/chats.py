from fastapi import APIRouter, HTTPException
from pydantic import BaseModel
from server.database import get_connection

router = APIRouter()

class ChatCreate(BaseModel):
    user1: str
    user2: str

class MessageSend(BaseModel):
    chat_id: int
    sender: str
    content: str

@router.post("/create")
def create_chat(chat: ChatCreate):
    conn = get_connection()
    cursor = conn.cursor()

    cursor.execute("SELECT id FROM users WHERE username = ?", (chat.user1,))
    user1_id = cursor.fetchone()
    cursor.execute("SELECT id FROM users WHERE username = ?", (chat.user2,))
    user2_id = cursor.fetchone()

    if not user1_id or not user2_id:
        raise HTTPException(status_code=404, detail="User not found")

    cursor.execute("""
        INSERT INTO chats (name, user1_id, user2_id)
        VALUES (?, ?, ?)
    """, (f"Chat: {chat.user1} & {chat.user2}", user1_id["id"], user2_id["id"]))
    chat_id = cursor.lastrowid

    cursor.execute("INSERT INTO participants (chat_id, user_id) VALUES (?, ?)", (chat_id, user1_id["id"]))
    cursor.execute("INSERT INTO participants (chat_id, user_id) VALUES (?, ?)", (chat_id, user2_id["id"]))

    conn.commit()
    conn.close()

    return {"chat_id": chat_id, "user1": chat.user1, "user2": chat.user2, "message": "Chat created"}

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

## Idk for what it was, but if you got an error with sending the message try to uncomment this code
# @router.post("/send")
# def send_message(message: MessageSend):
#     conn = get_connection()
#     cursor = conn.cursor()

#     cursor.execute("SELECT id FROM users WHERE username = ?", (message.sender,))
#     sender_id

@router.delete("/delete/{chat_id}")
def delete_chat(chat_id: int):
    conn = get_connection()
    cursor = conn.cursor()

    cursor.execute("SELECT * FROM chats WHERE id = ?", (chat_id,))
    chat = cursor.fetchone()
    if not chat:
        raise HTTPException(status_code=404, detail="Chat not found")

    cursor.execute("DELETE FROM messages WHERE chat_id = ?", (chat_id,))

    cursor.execute("DELETE FROM chats WHERE id = ?", (chat_id,))
    conn.commit()
    conn.close()

    return {"message": "Chat deleted"}    