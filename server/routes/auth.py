import sqlite3
from fastapi import APIRouter, HTTPException, Depends, status, File, UploadFile
from fastapi.security import OAuth2PasswordBearer
from pydantic import BaseModel
from jose import JWTError, jwt
from datetime import datetime, timedelta
from typing import Optional
import hashlib
import secrets
from server.database import get_connection
from pathlib import Path

router = APIRouter()

SECRET_KEY = "supersecretkey"  # REPLACE THIS KEY!!!!!
ALGORITHM = "HS256"
ACCESS_TOKEN_EXPIRE_MINUTES = 60

oauth2_scheme = OAuth2PasswordBearer(tokenUrl="auth/login")

class User(BaseModel):
    username: str
    password: str
    bio: Optional[str] = None

class UserUpdate(BaseModel):
    avatar_url: Optional[str] = None
    bio: Optional[str] = None

class Token(BaseModel):
    access_token: str
    token_type: str

def hash_password(password: str) -> str:
    return hashlib.sha256(password.encode()).hexdigest()

def create_access_token(user_id: int):
    expires_delta = timedelta(minutes=ACCESS_TOKEN_EXPIRE_MINUTES)
    payload = {
        "sub": str(user_id),
        "exp": datetime.utcnow() + expires_delta
    }
    token = jwt.encode(payload, SECRET_KEY, algorithm=ALGORITHM)
    return token

def verify_token(token: str):
    try:
        payload = jwt.decode(token, SECRET_KEY, algorithms=[ALGORITHM])
        user_id = payload.get("sub")
        if user_id is None:
            return None
        conn = get_connection()
        cursor = conn.cursor()
        cursor.execute("SELECT id, username, avatar_url, bio FROM users WHERE id = ?", (user_id,))
        user = cursor.fetchone()
        conn.close()
        if user:
            return {"id": user[0], "username": user[1], "avatar_url": user[2], "bio": user[3]}
        return None
    except JWTError:
        return None    

def get_user_by_id(user_id: int):
    conn = get_connection()
    cursor = conn.cursor()
    cursor.execute("SELECT id, username, avatar_url, bio FROM users WHERE id = ?", (user_id,))
    row = cursor.fetchone()
    conn.close()
    if row:
        return {"id": row[0], "username": row[1], "avatar_url": row[2], "bio": row[3]}
    return None

async def get_current_user(token: str = Depends(oauth2_scheme)):
    try:
        payload = jwt.decode(token, SECRET_KEY, algorithms=[ALGORITHM])
        user_id = int(payload.get("sub"))
        user = get_user_by_id(user_id)
        if not user:
            raise HTTPException(status_code=401, detail="Invalid token")
        return user
    except JWTError:
        raise HTTPException(status_code=401, detail="Invalid token")

@router.post("/register", response_model=Token)
def register(user: User):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        hashed_password = hash_password(user.password)
        cursor.execute(
            "INSERT INTO users (username, password, bio) VALUES (?, ?, ?)",
            (user.username, hashed_password, user.bio or "")
        )
        conn.commit()
        user_id = cursor.lastrowid
    except sqlite3.IntegrityError:
        conn.close()
        raise HTTPException(status_code=400, detail="User already exists")
    finally:
        conn.close()
    
    token = create_access_token(user_id)
    return {"access_token": token, "token_type": "bearer"}

@router.post("/login", response_model=Token)
def login(user: User):
    conn = get_connection()
    cursor = conn.cursor()
    cursor.execute("SELECT id, password FROM users WHERE username = ?", (user.username,))
    db_user = cursor.fetchone()
    conn.close()
    if not db_user or hash_password(user.password) != db_user[1]:
        raise HTTPException(status_code=401, detail="Incorrect username or password")
    
    token = create_access_token(db_user[0])
    return {"access_token": token, "token_type": "bearer"}

@router.get("/me")
async def get_me(current_user: dict = Depends(get_current_user)):
    return {
        "username": current_user["username"],
        "avatar_url": current_user["avatar_url"],
        "bio": current_user["bio"]
    }

@router.put("/me")
async def update_user_profile(update: UserUpdate = None, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    updates = []
    values = []
    # Если update не передан (None), ничего не обновляем, но не возвращаем ошибку
    if update:
        if update.avatar_url is not None:
            updates.append("avatar_url = ?")
            values.append(update.avatar_url)
        if update.bio is not None:
            updates.append("bio = ?")
            values.append(update.bio)
    
    # Если есть что обновлять, выполняем запрос
    if updates:
        query = f"UPDATE users SET {', '.join(updates)} WHERE id = ?"
        values.append(current_user["id"])
        cursor.execute(query, values)
        conn.commit()
    conn.close()
    return {"message": "Profile updated" if updates else "No updates provided"}

@router.post("/me/avatar")
async def upload_avatar(file: UploadFile = File(...), current_user: dict = Depends(get_current_user)):
    username = current_user["username"]
    upload_dir = Path(f"static/avatars/{username}")
    upload_dir.mkdir(parents=True, exist_ok=True)
    file_path = upload_dir / file.filename
    with file_path.open("wb") as buffer:
        buffer.write(await file.read())
    avatar_url = f"/static/avatars/{username}/{file.filename}"
    conn = get_connection()
    cursor = conn.cursor()
    cursor.execute("UPDATE users SET avatar_url = ? WHERE id = ?", (avatar_url, current_user["id"]))
    conn.commit()
    conn.close()
    return {"avatar_url": avatar_url}

@router.get("/users/{username}")
async def get_user_avatar(username: str):
    conn = get_connection()
    cursor = conn.cursor()
    cursor.execute("SELECT avatar_url, bio FROM users WHERE username = ?", (username,))
    user = cursor.fetchone()
    conn.close()
    if not user or not user[0]:  # user[0] — avatar_url
        return {"avatar_url": "/static/avatars/default.jpg", "bio": user[1] if user else ""}
    return {"avatar_url": user[0], "bio": user[1] or ""}