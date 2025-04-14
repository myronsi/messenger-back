from fastapi import UploadFile, File, APIRouter, HTTPException, Depends
from server.database import get_connection
from server.routes.auth import verify_token
import os
from pathlib import Path

router = APIRouter()

DEFAULT_AVATAR = "/static/avatars/default.jpg"

@router.post("/users/me/avatar")
async def upload_avatar(file: UploadFile = File(...), user: dict = Depends(verify_token)):
    upload_dir = Path("static/avatars")
    upload_dir.mkdir(parents=True, exist_ok=True)
    file_path = upload_dir / f"{user['id']}_{file.filename}"
    
    with file_path.open("wb") as buffer:
        buffer.write(await file.read())
    
    avatar_url = f"/static/avatars/{user['id']}_{file.filename}"
    conn = get_connection()
    cursor = conn.cursor()
    cursor.execute("UPDATE users SET avatar_url = ? WHERE id = ?", (avatar_url, user["id"]))
    conn.commit()
    conn.close()
    
    return {"avatar_url": avatar_url}

@router.get("/avatar/{username}")
async def get_user_avatar(username: str):
    conn = get_connection()
    cursor = conn.cursor()
    cursor.execute("SELECT avatar_url FROM users WHERE username = ?", (username,))
    row = cursor.fetchone()
    conn.close()
    if not row or not row[0]:
        raise HTTPException(status_code=404, detail="Avatar not found")
    return {"avatar_url": row[0]}

@router.get("/users/{username}")
async def get_user_profile(username: str):
    conn = get_connection()
    cursor = conn.cursor()
    cursor.execute("SELECT avatar_url, bio FROM users WHERE username = ?", (username,))
    row = cursor.fetchone()
    conn.close()
    if not row:
        raise HTTPException(status_code=404, detail="User not found")
    return {
        "avatar_url": row[0] or DEFAULT_AVATAR,
        "bio": row[1] or ""
    }