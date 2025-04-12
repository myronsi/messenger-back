from fastapi import APIRouter, HTTPException, Depends
from pydantic import BaseModel
from server.database import get_connection
from server.routes.auth import get_current_user

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

    try:
        # Проверяем существование пользователей
        cursor.execute("SELECT id FROM users WHERE username = ?", (chat.user1,))
        user1_id = cursor.fetchone()
        cursor.execute("SELECT id FROM users WHERE username = ?", (chat.user2,))
        user2_id = cursor.fetchone()

        if not user1_id or not user2_id:
            raise HTTPException(status_code=404, detail="One or both users not found")
        if chat.user1 == chat.user2:
            raise HTTPException(status_code=400, detail="Cannot create chat with yourself")

        # Проверяем, не существует ли уже чат между этими пользователями
        cursor.execute("""
            SELECT id FROM chats
            WHERE (user1_id = ? AND user2_id = ?) OR (user1_id = ? AND user2_id = ?)
        """, (user1_id["id"], user2_id["id"], user2_id["id"], user1_id["id"]))
        existing_chat = cursor.fetchone()
        if existing_chat:
            raise HTTPException(status_code=400, detail="Chat between these users already exists")

        # Создаём чат
        cursor.execute("""
            INSERT INTO chats (name, user1_id, user2_id)
            VALUES (?, ?, ?)
        """, (f"Chat: {chat.user1} & {chat.user2}", user1_id["id"], user2_id["id"]))
        chat_id = cursor.lastrowid

        # Добавляем участников
        cursor.execute("INSERT INTO participants (chat_id, user_id) VALUES (?, ?)", (chat_id, user1_id["id"]))
        cursor.execute("INSERT INTO participants (chat_id, user_id) VALUES (?, ?)", (chat_id, user2_id["id"]))

        conn.commit()
        return {"chat_id": chat_id, "user1": chat.user1, "user2": chat.user2, "message": "Chat created"}
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        raise HTTPException(status_code=500, detail=f"Error creating chat: {str(e)}")
    finally:
        conn.close()

@router.get("/list/{username}")
def list_chats(username: str, current_user: dict = Depends(get_current_user)):
    if username != current_user["username"]:
        raise HTTPException(status_code=403, detail="You can only view your own chats")

    conn = get_connection()
    cursor = conn.cursor()

    try:
        cursor.execute("SELECT id FROM users WHERE username = ?", (username,))
        user_id = cursor.fetchone()
        if not user_id:
            raise HTTPException(status_code=404, detail="User not found")

        cursor.execute("""
            SELECT chats.id, chats.name, chats.user1_id, chats.user2_id
            FROM chats
            JOIN participants ON chats.id = participants.chat_id
            WHERE participants.user_id = ?
        """, (user_id["id"],))
        chats = cursor.fetchall()

        chat_list = []
        for chat in chats:
            interlocutor_id = chat["user2_id"] if chat["user1_id"] == user_id["id"] else chat["user1_id"]
            cursor.execute("SELECT username, avatar_url FROM users WHERE id = ?", (interlocutor_id,))
            interlocutor = cursor.fetchone()

            if interlocutor:
                interlocutor_name = interlocutor["username"]
                avatar_url = interlocutor["avatar_url"] or "/static/avatars/default.jpg"
                interlocutor_deleted = False
            else:
                interlocutor_name = "удалённый аккаунт"
                avatar_url = "/static/avatars/default.jpg"
                interlocutor_deleted = True

            chat_list.append({
                "id": chat["id"],
                "name": chat["name"],
                "interlocutor_name": interlocutor_name,
                "avatar_url": avatar_url,
                "interlocutor_deleted": interlocutor_deleted
            })

        return {"chats": chat_list}
    finally:
        conn.close()

@router.delete("/delete/{chat_id}")
def delete_chat(chat_id: int, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        # Проверяем, существует ли чат
        cursor.execute("SELECT user1_id, user2_id FROM chats WHERE id = ?", (chat_id,))
        chat = cursor.fetchone()
        if not chat:
            raise HTTPException(status_code=404, detail="Chat not found")

        # Проверяем, является ли пользователь участником чата
        if current_user["id"] not in (chat["user1_id"], chat["user2_id"]):
            raise HTTPException(status_code=403, detail="You are not a member of this chat")

        # Удаляем записи из participants
        cursor.execute("DELETE FROM participants WHERE chat_id = ?", (chat_id,))
        # Удаляем чат
        cursor.execute("DELETE FROM chats WHERE id = ?", (chat_id,))

        conn.commit()
        return {"message": "Chat deleted"}
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        raise HTTPException(status_code=500, detail=f"Error deleting chat: {str(e)}")
    finally:
        conn.close()