from fastapi import APIRouter, Depends, File, HTTPException, UploadFile
from pydantic import BaseModel
from server.database import get_connection
from server.routes.auth import get_current_user
from server.websocket import manager
from server.chat_summary import get_chat_unread_summary
from server.privacy import can_invite_to_group, serialize_user
import logging
from pathlib import Path
from uuid import uuid4

router = APIRouter()

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

GROUP_ROLES = {"owner", "admin", "moderator", "member"}
ASSIGNABLE_ROLES = {"admin", "moderator", "member"}
DEFAULT_GROUP_AVATAR = "/static/avatars/group.png"
DEFAULT_AVATAR = "/static/avatars/default.jpg"


class GroupCreate(BaseModel):
    name: str
    description: str | None = None
    participants: list[str]


class GroupUpdate(BaseModel):
    name: str | None = None
    description: str | None = None


class GroupParticipantAdd(BaseModel):
    username: str


class GroupRoleUpdate(BaseModel):
    role: str


class GroupOwnerTransfer(BaseModel):
    username: str


def _normalize_role(role: str | None) -> str:
    normalized = (role or "member").strip().lower()
    return normalized if normalized in GROUP_ROLES else "member"


def _permissions_for_role(role: str | None):
    role = _normalize_role(role)
    return {
        "can_edit_group": role in {"owner", "admin"},
        "can_manage_participants": role in {"owner", "admin"},
        "can_assign_roles": role in {"owner", "admin"},
        "can_delete_any_message": role in {"owner", "admin", "moderator"},
        "can_delete_group": role == "owner",
        "can_transfer_ownership": role == "owner",
    }


def _get_member_role(cursor, chat_id: int, user_id: int) -> str | None:
    cursor.execute("""
        SELECT p.role, g.admin_id
        FROM participants p
        JOIN groups g ON g.chat_id = p.chat_id
        WHERE p.chat_id = ? AND p.user_id = ?
    """, (chat_id, user_id))
    row = cursor.fetchone()
    if not row:
        return None
    if row["admin_id"] == user_id:
        return "owner"
    return _normalize_role(row["role"])


def _ensure_member(cursor, chat_id: int, user_id: int):
    role = _get_member_role(cursor, chat_id, user_id)
    if not role:
        raise HTTPException(status_code=403, detail="You are not a member of this group")
    return role


def _ensure_role(cursor, chat_id: int, user_id: int, allowed_roles: set[str], detail: str):
    role = _ensure_member(cursor, chat_id, user_id)
    if role not in allowed_roles:
        raise HTTPException(status_code=403, detail=detail)
    return role


def _get_group_details(cursor, chat_id: int, current_user_id: int | None = None):
    cursor.execute("""
        SELECT
            c.id AS chat_id,
            c.name,
            c.description,
            c.avatar_url,
            g.admin_id AS owner_id,
            owner.username AS owner_username
        FROM chats c
        JOIN groups g ON g.chat_id = c.id
        JOIN users owner ON owner.id = g.admin_id
        WHERE c.id = ? AND c.type = 'group'
    """, (chat_id,))
    group = cursor.fetchone()
    if not group:
        return None

    cursor.execute("""
        SELECT u.id, u.username, u.display_name, u.avatar_url, p.role
        FROM participants p
        JOIN users u ON u.id = p.user_id
        WHERE p.chat_id = ?
        ORDER BY
          CASE WHEN u.id = ? THEN 0 ELSE 1 END,
          u.username COLLATE NOCASE
    """, (chat_id, group["owner_id"]))

    participants = []
    current_user_role = None
    for row in cursor.fetchall():
        role = "owner" if row["id"] == group["owner_id"] else _normalize_role(row["role"])
        if current_user_id and row["id"] == current_user_id:
            current_user_role = role
        serialized_user = serialize_user(cursor, {
            "id": row["id"],
            "username": row["username"],
            "display_name": row["display_name"],
            "avatar_url": row["avatar_url"],
            "bio": "",
            "last_seen": None,
        }, current_user_id)
        participants.append({
            "id": row["id"],
            "username": serialized_user["username"],
            "display_name": serialized_user["display_name"],
            "avatar_url": serialized_user["avatar_url"],
            "role": role,
            "is_owner": role == "owner",
            "is_admin": role in {"owner", "admin"},
        })

    group_dict = dict(group)
    current_user_role = current_user_role or (
        _get_member_role(cursor, chat_id, current_user_id) if current_user_id else None
    )
    return {
        "chat_id": group_dict["chat_id"],
        "name": group_dict["name"],
        "description": group_dict["description"] or "",
        "avatar_url": group_dict["avatar_url"] or DEFAULT_GROUP_AVATAR,
        "owner_id": group_dict["owner_id"],
        "owner_username": group_dict["owner_username"],
        "admin_id": group_dict["owner_id"],
        "admin_username": group_dict["owner_username"],
        "current_user_role": current_user_role,
        "permissions": _permissions_for_role(current_user_role),
        "participants": participants,
    }


async def _broadcast_group_update(cursor, chat_id: int, removed_username: str | None = None):
    group = _get_group_details(cursor, chat_id)
    payload = {"type": "group_updated", "group": group}
    if removed_username:
        payload["removed_username"] = removed_username
    await manager.broadcast(0, {"type": "group_updated", "group": group})
    await manager.broadcast(chat_id, payload)
    return group


@router.post("/create")
async def create_group(group: GroupCreate, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        participant_ids = []
        participant_usernames = []
        for username in group.participants:
            cursor.execute("SELECT id, username FROM users WHERE username = ?", (username,))
            user = cursor.fetchone()
            if not user:
                raise HTTPException(status_code=404, detail=f"User {username} not found")
            if not can_invite_to_group(cursor, current_user["id"], user["id"]):
                raise HTTPException(status_code=403, detail=f"User {username} does not allow group invites from you")
            participant_ids.append(user["id"])
            participant_usernames.append(user["username"])

        creator_id = current_user["id"]
        creator_username = current_user["username"]
        if creator_id not in participant_ids:
            participant_ids.append(creator_id)
            participant_usernames.append(creator_username)

        group_name = group.name.strip()
        if not group_name:
            raise HTTPException(status_code=400, detail="Group name cannot be empty")

        cursor.execute("""
            INSERT INTO chats (name, type, description, avatar_url)
            VALUES (?, 'group', ?, ?)
        """, (group_name, (group.description or "").strip(), DEFAULT_GROUP_AVATAR))
        chat_id = cursor.lastrowid

        cursor.execute("""
            INSERT INTO groups (chat_id, admin_id)
            VALUES (?, ?)
        """, (chat_id, creator_id))

        for user_id in set(participant_ids):
            role = "owner" if user_id == creator_id else "member"
            cursor.execute(
                "INSERT INTO participants (chat_id, user_id, role) VALUES (?, ?, ?)",
                (chat_id, user_id, role),
            )

        conn.commit()

        message = {
            "type": "group_created",
            "group": {
                "chat_id": chat_id,
                "name": group_name,
                "participants": list(set(participant_usernames)),
            },
        }
        await manager.broadcast(0, message)
        logger.info(f"Sent group_created notification for chat_id={chat_id} to chat_id=0")

        return {"chat_id": chat_id, "name": group_name, "message": "Group created successfully"}
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
            SELECT c.id, c.name, c.type, c.description, c.avatar_url
            FROM chats c
            JOIN participants p ON c.id = p.chat_id
            JOIN users u ON p.user_id = u.id
            WHERE u.username = ? AND c.type = 'group'
        """, (username,))
        groups = cursor.fetchall()

        return {
            "groups": [
                {
                    "chat_id": group["id"],
                    "name": group["name"],
                    "type": group["type"],
                    "description": group["description"] or "",
                    "avatar_url": group["avatar_url"] or DEFAULT_GROUP_AVATAR,
                    "is_pinned": cursor.execute(
                        "SELECT 1 FROM user_chat_pins WHERE user_id = ? AND chat_id = ?",
                        (current_user["id"], group["id"]),
                    ).fetchone() is not None,
                    **get_chat_unread_summary(cursor, group["id"], current_user["id"]),
                }
                for group in groups
            ]
        }
    except Exception as e:
        logger.error(f"Error fetching groups: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error fetching groups: {str(e)}")
    finally:
        conn.close()


@router.get("/{chat_id}")
async def get_group(chat_id: int, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        group = _get_group_details(cursor, chat_id, current_user["id"])
        if not group:
            raise HTTPException(status_code=404, detail="Group not found")
        _ensure_member(cursor, chat_id, current_user["id"])
        return group
    finally:
        conn.close()


@router.patch("/{chat_id}")
async def update_group(chat_id: int, payload: GroupUpdate, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        _ensure_role(cursor, chat_id, current_user["id"], {"owner", "admin"}, "Only owners and admins can update this group")

        updates = []
        params = []
        if payload.name is not None:
            name = payload.name.strip()
            if not name:
                raise HTTPException(status_code=400, detail="Group name cannot be empty")
            updates.append("name = ?")
            params.append(name)
        if payload.description is not None:
            updates.append("description = ?")
            params.append(payload.description.strip())

        if updates:
            params.append(chat_id)
            cursor.execute(f"UPDATE chats SET {', '.join(updates)} WHERE id = ? AND type = 'group'", params)
            conn.commit()

        group = await _broadcast_group_update(cursor, chat_id)
        return _get_group_details(cursor, chat_id, current_user["id"]) or group
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error updating group {chat_id}: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error updating group: {str(e)}")
    finally:
        conn.close()


@router.post("/{chat_id}/avatar")
async def upload_group_avatar(
    chat_id: int,
    file: UploadFile = File(...),
    current_user: dict = Depends(get_current_user),
):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        _ensure_role(cursor, chat_id, current_user["id"], {"owner", "admin"}, "Only owners and admins can update this group")

        suffix = Path(file.filename or "group-avatar").suffix
        safe_name = f"group_{chat_id}_{uuid4().hex}{suffix}"
        upload_dir = Path("static/avatars/groups")
        upload_dir.mkdir(parents=True, exist_ok=True)
        file_path = upload_dir / safe_name
        file_path.write_bytes(await file.read())

        avatar_url = f"/static/avatars/groups/{safe_name}"
        cursor.execute("UPDATE chats SET avatar_url = ? WHERE id = ? AND type = 'group'", (avatar_url, chat_id))
        conn.commit()

        group = await _broadcast_group_update(cursor, chat_id)
        return _get_group_details(cursor, chat_id, current_user["id"]) or group
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error uploading group avatar for {chat_id}: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error uploading group avatar: {str(e)}")
    finally:
        conn.close()


@router.post("/{chat_id}/participants")
async def add_group_participant(
    chat_id: int,
    payload: GroupParticipantAdd,
    current_user: dict = Depends(get_current_user),
):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        _ensure_role(cursor, chat_id, current_user["id"], {"owner", "admin"}, "Only owners and admins can add participants")

        username = payload.username.strip()
        if not username:
            raise HTTPException(status_code=400, detail="Username is required")

        cursor.execute("SELECT id, username FROM users WHERE username = ?", (username,))
        user = cursor.fetchone()
        if not user:
            raise HTTPException(status_code=404, detail="User not found")
        if not can_invite_to_group(cursor, current_user["id"], user["id"]):
            raise HTTPException(status_code=403, detail="This user does not allow group invites from you")

        cursor.execute(
            "INSERT OR IGNORE INTO participants (chat_id, user_id, role) VALUES (?, ?, 'member')",
            (chat_id, user["id"]),
        )
        conn.commit()

        group = await _broadcast_group_update(cursor, chat_id)
        return _get_group_details(cursor, chat_id, current_user["id"]) or group
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error adding participant to group {chat_id}: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error adding participant: {str(e)}")
    finally:
        conn.close()


@router.patch("/{chat_id}/participants/{username}/role")
async def update_group_participant_role(
    chat_id: int,
    username: str,
    payload: GroupRoleUpdate,
    current_user: dict = Depends(get_current_user),
):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        _ensure_role(cursor, chat_id, current_user["id"], {"owner", "admin"}, "Only owners and admins can assign roles")
        next_role = payload.role.strip().lower()
        if next_role not in ASSIGNABLE_ROLES:
            raise HTTPException(status_code=400, detail="Role must be admin, moderator, or member")

        cursor.execute("""
            SELECT u.id, p.role, g.admin_id
            FROM users u
            JOIN participants p ON p.user_id = u.id AND p.chat_id = ?
            JOIN groups g ON g.chat_id = p.chat_id
            WHERE u.username = ?
        """, (chat_id, username))
        target = cursor.fetchone()
        if not target:
            raise HTTPException(status_code=404, detail="Participant not found")
        if target["admin_id"] == target["id"]:
            raise HTTPException(status_code=400, detail="Transfer ownership before changing the owner role")

        cursor.execute(
            "UPDATE participants SET role = ? WHERE chat_id = ? AND user_id = ?",
            (next_role, chat_id, target["id"]),
        )
        conn.commit()

        group = await _broadcast_group_update(cursor, chat_id)
        return _get_group_details(cursor, chat_id, current_user["id"]) or group
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error updating participant role in group {chat_id}: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error updating participant role: {str(e)}")
    finally:
        conn.close()


@router.post("/{chat_id}/transfer-owner")
async def transfer_group_owner(
    chat_id: int,
    payload: GroupOwnerTransfer,
    current_user: dict = Depends(get_current_user),
):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        _ensure_role(cursor, chat_id, current_user["id"], {"owner"}, "Only the owner can transfer ownership")

        cursor.execute("""
            SELECT u.id, u.username
            FROM users u
            JOIN participants p ON p.user_id = u.id
            WHERE p.chat_id = ? AND u.username = ?
        """, (chat_id, payload.username.strip()))
        target = cursor.fetchone()
        if not target:
            raise HTTPException(status_code=404, detail="Participant not found")
        if target["id"] == current_user["id"]:
            raise HTTPException(status_code=400, detail="You already own this group")

        cursor.execute("UPDATE groups SET admin_id = ? WHERE chat_id = ?", (target["id"], chat_id))
        cursor.execute("UPDATE participants SET role = 'admin' WHERE chat_id = ? AND user_id = ?", (chat_id, current_user["id"]))
        cursor.execute("UPDATE participants SET role = 'owner' WHERE chat_id = ? AND user_id = ?", (chat_id, target["id"]))
        conn.commit()

        group = await _broadcast_group_update(cursor, chat_id)
        return _get_group_details(cursor, chat_id, current_user["id"]) or group
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error transferring group owner for {chat_id}: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error transferring ownership: {str(e)}")
    finally:
        conn.close()


@router.delete("/{chat_id}/participants/{username}")
async def remove_group_participant(
    chat_id: int,
    username: str,
    current_user: dict = Depends(get_current_user),
):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        _ensure_role(cursor, chat_id, current_user["id"], {"owner", "admin"}, "Only owners and admins can remove participants")

        cursor.execute("""
            SELECT u.id, p.role, g.admin_id
            FROM users u
            JOIN participants p ON p.user_id = u.id AND p.chat_id = ?
            JOIN groups g ON g.chat_id = p.chat_id
            WHERE u.username = ?
        """, (chat_id, username))
        user = cursor.fetchone()
        if not user:
            raise HTTPException(status_code=404, detail="User not found")
        if user["admin_id"] == user["id"]:
            raise HTTPException(status_code=400, detail="Cannot remove the group owner")

        cursor.execute("DELETE FROM participants WHERE chat_id = ? AND user_id = ?", (chat_id, user["id"]))
        conn.commit()

        group_details = await _broadcast_group_update(cursor, chat_id, removed_username=username)
        return _get_group_details(cursor, chat_id, current_user["id"]) or group_details
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error removing participant from group {chat_id}: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error removing participant: {str(e)}")
    finally:
        conn.close()


@router.delete("/{chat_id}/leave")
async def leave_group(chat_id: int, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        role = _ensure_member(cursor, chat_id, current_user["id"])
        if role == "owner":
            raise HTTPException(status_code=400, detail="Transfer ownership before leaving the group")

        cursor.execute("DELETE FROM participants WHERE chat_id = ? AND user_id = ?", (chat_id, current_user["id"]))
        conn.commit()

        group_details = await _broadcast_group_update(cursor, chat_id, removed_username=current_user["username"])
        return {"message": "Left group successfully", "group": group_details}
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error leaving group {chat_id}: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error leaving group: {str(e)}")
    finally:
        conn.close()


@router.delete("/delete/{chat_id}")
async def delete_group(chat_id: int, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        _ensure_role(cursor, chat_id, current_user["id"], {"owner"}, "Only the group owner can delete the group")

        cursor.execute("DELETE FROM participants WHERE chat_id = ?", (chat_id,))
        cursor.execute("DELETE FROM messages WHERE chat_id = ?", (chat_id,))
        cursor.execute("DELETE FROM groups WHERE chat_id = ?", (chat_id,))
        cursor.execute("DELETE FROM chats WHERE id = ?", (chat_id,))

        conn.commit()

        message = {"type": "chat_deleted", "chat_id": chat_id}
        await manager.broadcast(0, message)
        await manager.broadcast(chat_id, message)
        logger.info(f"Sent chat_deleted notification for chat_id={chat_id}")

        return {"message": "Group deleted successfully"}
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error deleting group: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error deleting group: {str(e)}")
    finally:
        conn.close()
