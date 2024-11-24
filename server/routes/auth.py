import sqlite3
from fastapi import APIRouter, HTTPException
from pydantic import BaseModel
from server.database import get_connection
import hashlib

router = APIRouter()

# Модель данных для пользователя
class User(BaseModel):
    username: str
    password: str

# Хэширование паролей
def hash_password(password: str) -> str:
    return hashlib.sha256(password.encode()).hexdigest()

# Регистрация пользователя
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
        raise HTTPException(status_code=400, detail="Пользователь уже существует")
    finally:
        conn.close()
    return {"message": "Пользователь успешно зарегистрирован"}

# Авторизация пользователя
@router.post("/login")
def login(user: User):
    conn = get_connection()
    cursor = conn.cursor()
    cursor.execute("SELECT * FROM users WHERE username = ?", (user.username,))
    db_user = cursor.fetchone()
    conn.close()
    if not db_user or hash_password(user.password) != db_user["password"]:
        raise HTTPException(status_code=400, detail="Неверный логин или пароль")
    return {"message": "Успешный вход"}
