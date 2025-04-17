from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel
from server.database import get_connection
from server.routes.auth import get_current_user
from server.websocket import manager
import logging

router = APIRouter()

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

class GroupCreate(BaseModel):
    name: str
    participants: list[str]

@router.post("/create")
async def create_group(group: GroupCreate, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        # Validate participants
        participant_ids = []
        participant_usernames = []
        for username in group.participants:
            cursor.execute("SELECT id, username FROM users WHERE username = ?", (username,))
            user = cursor.fetchone()
            if not user:
                raise HTTPException(status_code=404, detail=f"User {username} not found")
            participant_ids.append(user["id"])
            participant_usernames.append(user["username"])

        # Include the creator if not already in participants
        creator_id = current_user["id"]
        creator_username = current_user["username"]
        if creator_id not in participant_ids:
            participant_ids.append(creator_id)
            participant_usernames.append(creator_username)

        # Create chat entry
        cursor.execute("""
            INSERT INTO chats (name, type)
            VALUES (?, 'group')
        """, (group.name,))
        chat_id = cursor.lastrowid

        # Create group entry
        cursor.execute("""
            INSERT INTO groups (chat_id, admin_id)
            VALUES (?, ?)
        """, (chat_id, creator_id))

        # Add participants
        for user_id in set(participant_ids):  # Avoid duplicates
            cursor.execute("INSERT INTO participants (chat_id, user_id) VALUES (?, ?)", (chat_id, user_id))

        conn.commit()

        # Notify via WebSocket
        message = {
            "type": "group_created",
            "group": {
                "chat_id": chat_id,
                "name": group.name,
                "participants": list(set(participant_usernames))
            }
        }
        await manager.broadcast(0, message)  # Broadcast to chat_id=0 for chat list updates
        logger.info(f"Sent group_created notification for chat_id={chat_id} to chat_id=0")

        return {"chat_id": chat_id, "name": group.name, "message": "Group created successfully"}
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error creating group: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error creating group: {str(e)}")
    finally:
        conn.close()

@router.get("/list/{username}")
async def list_groups(username: str, current_user: dict = Depends(get_current_user)):
    if current_user["username"] != username:
        raise HTTPException(status_code=403, detail="You can only view your own groups")

    conn = get_connection()
    cursor = conn.cursor()

    try:
        cursor.execute("""
            SELECT c.id, c.name, c.type
            FROM chats c
            JOIN participants p ON c.id = p.chat_id
            JOIN users u ON p.user_id = u.id
            WHERE u.username = ? AND c.type = 'group'
        """, (username,))
        groups = cursor.fetchall()

        return {
            "groups": [
                {"chat_id": group["id"], "name": group["name"], "type": group["type"]}
                for group in groups
            ]
        }
    except Exception as e:
        logger.error(f"Error fetching groups: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error fetching groups: {str(e)}")
    finally:
        conn.close()

@router.delete("/delete/{chat_id}")
async def delete_group(chat_id: int, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        # Check if chat exists and user is admin
        cursor.execute("""
            SELECT g.admin_id
            FROM groups g
            JOIN chats c ON g.chat_id = c.id
            WHERE g.chat_id = ? AND c.type = 'group'
        """, (chat_id,))
        group = cursor.fetchone()
        if not group:
            raise HTTPException(status_code=404, detail="Group not found")
        if group["admin_id"] != current_user["id"]:
            raise HTTPException(status_code=403, detail="Only the group admin can delete the group")

        # Delete related data
        cursor.execute("DELETE FROM participants WHERE chat_id = ?", (chat_id,))
        cursor.execute("DELETE FROM messages WHERE chat_id = ?", (chat_id,))
        cursor.execute("DELETE FROM groups WHERE chat_id = ?", (chat_id,))
        cursor.execute("DELETE FROM chats WHERE id = ?", (chat_id,))

        conn.commit()

        # Notify via WebSocket
        message = {"type": "chat_deleted", "chat_id": chat_id}
        await manager.broadcast(0, message)  # Broadcast to chat_id=0 for chat list updates
        logger.info(f"Sent chat_deleted notification for chat_id={chat_id} to chat_id=0")

        return {"message": "Group deleted successfully"}
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error deleting group: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error deleting group: {str(e)}")
    finally:
        conn.close()