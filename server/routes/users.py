from fastapi import APIRouter, HTTPException, Depends
from pydantic import BaseModel
from server.database import get_connection
from server.routes.auth import get_current_user
from server.presence import is_user_online
from server.time_utils import to_utc_iso
from server.usernames import escape_like
from server.avatar_history import get_avatar_history
from server.privacy import DEFAULT_AVATAR, can_target_be_searched, get_privacy_settings, serialize_user, visibility_allows
import os

router = APIRouter()


class ContactDisplayNameUpdate(BaseModel):
    display_name: str | None = None


def normalize_contact_display_name(value: str | None) -> str | None:
    normalized = " ".join((value or "").strip().split())
    if not normalized:
        return None
    if len(normalized) > 50:
        raise HTTPException(status_code=400, detail="Custom name must be 50 characters or less")
    return normalized

@router.get("/avatar/{username}")
async def get_user_avatar(username: str, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("SELECT id, username, display_name, avatar_url, bio, created_at, last_seen FROM users WHERE username = ?", (username,))
        row = cursor.fetchone()
        if not row:
            raise HTTPException(status_code=404, detail="Avatar not found")
        return {"avatar_url": serialize_user(cursor, row, current_user["id"])["avatar_url"]}
    finally:
        conn.close()

@router.get("/users/{username}/avatars")
async def get_user_avatar_history(username: str, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("SELECT id FROM users WHERE username = ?", (username,))
        user = cursor.fetchone()
        if not user:
            raise HTTPException(status_code=404, detail="User not found")
        settings = get_privacy_settings(cursor, user["id"])
        if not visibility_allows(cursor, current_user["id"], user["id"], settings["avatar_visibility"], "avatar_visibility"):
            return {"avatars": []}
        history = get_avatar_history(username)
        if history is None:
            raise HTTPException(status_code=404, detail="User not found")
        return history
    finally:
        conn.close()

@router.get("/users/{username}")
async def get_user_profile(username: str, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("SELECT id, username, display_name, avatar_url, bio, created_at, last_seen FROM users WHERE username = ?", (username,))
        row = cursor.fetchone()
        if not row:
            raise HTTPException(status_code=404, detail="User not found")
        return serialize_user(cursor, row, current_user["id"])
    finally:
        conn.close()


@router.put("/users/{username}/contact-name")
async def update_contact_display_name(username: str, payload: ContactDisplayNameUpdate, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("SELECT id, username, display_name, avatar_url, bio, created_at, last_seen FROM users WHERE username = ?", (username,))
        target = cursor.fetchone()
        if not target:
            raise HTTPException(status_code=404, detail="User not found")
        if target["id"] == current_user["id"]:
            raise HTTPException(status_code=400, detail="Use profile settings to change your own display name")

        display_name = normalize_contact_display_name(payload.display_name)
        if display_name:
            cursor.execute(
                """
                INSERT INTO user_contact_names (owner_id, target_id, display_name, updated_at)
                VALUES (?, ?, ?, CURRENT_TIMESTAMP)
                ON CONFLICT(owner_id, target_id)
                DO UPDATE SET display_name = excluded.display_name, updated_at = CURRENT_TIMESTAMP
                """,
                (current_user["id"], target["id"], display_name),
            )
        else:
            cursor.execute(
                "DELETE FROM user_contact_names WHERE owner_id = ? AND target_id = ?",
                (current_user["id"], target["id"]),
            )
        conn.commit()
        return serialize_user(cursor, target, current_user["id"])
    finally:
        conn.close()


@router.delete("/users/{username}/contact-name")
async def delete_contact_display_name(username: str, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("SELECT id, username, display_name, avatar_url, bio, created_at, last_seen FROM users WHERE username = ?", (username,))
        target = cursor.fetchone()
        if not target:
            raise HTTPException(status_code=404, detail="User not found")
        cursor.execute(
            "DELETE FROM user_contact_names WHERE owner_id = ? AND target_id = ?",
            (current_user["id"], target["id"]),
        )
        conn.commit()
        return serialize_user(cursor, target, current_user["id"])
    finally:
        conn.close()

@router.get("/search")
async def search_users(q: str, current_user: dict = Depends(get_current_user)):
    """Search users by partial username."""
    conn = get_connection()
    cursor = conn.cursor()
    try:
        like_q = f"%{escape_like(q)}%"
        cursor.execute("SELECT id, username, display_name, avatar_url, bio, created_at, last_seen FROM users WHERE LOWER(username) LIKE LOWER(?) ESCAPE '\\' LIMIT 20", (like_q,))
        rows = cursor.fetchall()
        results = []
        for row in rows:
            if can_target_be_searched(cursor, current_user["id"], row["id"]):
                results.append(serialize_user(cursor, row, current_user["id"]))
        return {"users": results}
    finally:
        conn.close()


@router.get("/{id}")
async def get_user_profile_by_id(id: int, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("SELECT id, username, display_name, avatar_url, bio, created_at, last_seen FROM users WHERE id = ?", (id,))
        row = cursor.fetchone()
        if not row:
            raise HTTPException(status_code=404, detail="User not found")
        return serialize_user(cursor, row, current_user["id"])
    finally:
        conn.close()
