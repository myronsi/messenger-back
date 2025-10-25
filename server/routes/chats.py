from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel
from server.database import get_connection
from server.routes.auth import get_current_user
from server.websocket import manager
import logging

router = APIRouter()

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

class ChatCreate(BaseModel):
    user1: str
    user2: str

@router.post("/create")
async def create_chat(chat: ChatCreate, current_user: dict = Depends(get_current_user)):
    if chat.user1 != current_user["username"]:
        raise HTTPException(status_code=403, detail="You can only create chats as yourself")

    conn = get_connection()
    cursor = conn.cursor()

    try:
        # Check if users exist
        cursor.execute("SELECT id, avatar_url FROM users WHERE username = ?", (chat.user1,))
        user1 = cursor.fetchone()
        cursor.execute("SELECT id, avatar_url FROM users WHERE username = ?", (chat.user2,))
        user2 = cursor.fetchone()

        if not user1 or not user2:
            raise HTTPException(status_code=404, detail="One or both users not found")
        if chat.user1 == chat.user2:
            raise HTTPException(status_code=400, detail="Cannot create chat with yourself")

        # Check if chat already exists
        cursor.execute("""
            SELECT id FROM chats
            WHERE type = 'one-on-one' AND (
                (user1_id = ? AND user2_id = ?) OR
                (user1_id = ? AND user2_id = ?)
            )
        """, (user1["id"], user2["id"], user2["id"], user1["id"]))
        existing_chat = cursor.fetchone()
        if existing_chat:
            raise HTTPException(status_code=400, detail="Chat between these users already exists")

        # Create chat
        chat_name = f"{chat.user1} & {chat.user2}"
        cursor.execute("""
            INSERT INTO chats (name, type, user1_id, user2_id)
            VALUES (?, 'one-on-one', ?, ?)
        """, (chat_name, user1["id"], user2["id"]))
        chat_id = cursor.lastrowid

        # Add participants
        cursor.execute("INSERT INTO participants (chat_id, user_id) VALUES (?, ?)", (chat_id, user1["id"]))
        logger.info(f"Added participant: chat_id={chat_id}, user_id={user1['id']}")
        cursor.execute("INSERT INTO participants (chat_id, user_id) VALUES (?, ?)", (chat_id, user2["id"]))
        logger.info(f"Added participant: chat_id={chat_id}, user_id={user2['id']}")

        conn.commit()

        # Prepare WebSocket notification
        chat_data = {
            "type": "chat_created",
            "chat": {
                "chat_id": chat_id,
                "name": chat_name,
                "user1": chat.user1,
                "user2": chat.user2,
                "user1_avatar_url": user1["avatar_url"] or "/static/avatars/default.jpg",
                "user2_avatar_url": user2["avatar_url"] or "/static/avatars/default.jpg"
            }
        }
        await manager.broadcast(0, chat_data)
        logger.info(f"Sent chat_created notification for chat_id={chat_id} to chat_id=0")

        return {
            "chat_id": chat_id,
            "message": "Chat created successfully"
        }
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error creating chat: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error creating chat: {str(e)}")
    finally:
        conn.close()

@router.get("/list/{username}")
async def list_chats(username: str, current_user: dict = Depends(get_current_user)):
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
            SELECT c.id, c.name, c.user1_id, c.user2_id,
                   u1.username AS user1_username, u1.avatar_url AS user1_avatar_url,
                   u2.username AS user2_username, u2.avatar_url AS user2_avatar_url
            FROM chats c
            JOIN participants p ON c.id = p.chat_id
            LEFT JOIN users u1 ON c.user1_id = u1.id
            LEFT JOIN users u2 ON c.user2_id = u2.id
            WHERE p.user_id = ? AND c.type = 'one-on-one'
        """, (user_id["id"],))
        chats = cursor.fetchall()

        chat_list = []
        for chat in chats:
            is_user1 = chat["user1_id"] == user_id["id"]
            interlocutor_username = chat["user2_username"] if is_user1 else chat["user1_username"]
            interlocutor_avatar = (
                chat["user2_avatar_url"] if is_user1 else chat["user1_avatar_url"]
            ) or "/static/avatars/default.jpg"
            interlocutor_deleted = not interlocutor_username

            chat_list.append({
                "id": chat["id"],
                "name": interlocutor_username or "Deleted User",
                "interlocutor_name": interlocutor_username or "Deleted User",
                "avatar_url": interlocutor_avatar,
                "interlocutor_deleted": interlocutor_deleted
            })

        return {"chats": chat_list}
    except Exception as e:
        logger.error(f"Error fetching chats: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error fetching chats: {str(e)}")
    finally:
        conn.close()

@router.delete("/delete/{chat_id}")
async def delete_chat(chat_id: int, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        # Check if chat exists and user is a participant
        cursor.execute("SELECT user1_id, user2_id FROM chats WHERE id = ? AND type = 'one-on-one'", (chat_id,))
        chat = cursor.fetchone()
        if not chat:
            raise HTTPException(status_code=404, detail="Chat not found")
        if current_user["id"] not in (chat["user1_id"], chat["user2_id"]):
            raise HTTPException(status_code=403, detail="You are not a member of this chat")

        # Delete related data
        cursor.execute("DELETE FROM participants WHERE chat_id = ?", (chat_id,))
        cursor.execute("DELETE FROM messages WHERE chat_id = ?", (chat_id,))
        cursor.execute("DELETE FROM chats WHERE id = ?", (chat_id,))

        conn.commit()

        # Notify via WebSocket
        message = {
            "type": "chat_deleted",
            "chat_id": chat_id
        }
        await manager.broadcast(0, message)
        logger.info(f"Sent chat_deleted notification for chat_id={chat_id} to chat_id=0")

        return {"message": "Chat deleted successfully"}
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error deleting chat: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error deleting chat: {str(e)}")
    finally:
        conn.close()