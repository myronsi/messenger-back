import sqlite3
from fastapi import APIRouter, HTTPException
from pydantic import BaseModel
from server.database import get_connection
import hashlib

router = APIRouter()

# Data model for user
class User(BaseModel):
    username: str
    password: str

# Password Hashing
def hash_password(password: str) -> str:
    return hashlib.sha256(password.encode()).hexdigest()

# User registration
@router.post("/register")
def register(user: User):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        hashed_password = hash_password(user.password)
        cursor.execute("INSERT INTO users (username, password) VALUES (?, ?)",
                       (user.username, hashed_password))
        conn.commit()
    except sqlite3.IntegrityError:
        raise HTTPException(status_code=400, detail="The user already exists")
    finally:
        conn.close()
    return {"message": "User registered successfully"}
    
# User authorization
@router.post("/login")
def login(user: User):
    conn = get_connection()
    cursor = conn.cursor()
    cursor.execute("SELECT * FROM users WHERE username = ?", (user.username,))
    db_user = cursor.fetchone()
    conn.close()
    if not db_user or hash_password(user.password) != db_user["password"]:
        raise HTTPException(status_code=400, detail="Incorrect login or password")
    return {"message": "Successful login"}
