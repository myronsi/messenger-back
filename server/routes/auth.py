import sqlite3
from fastapi import APIRouter, HTTPException, Depends, status, File, UploadFile
from fastapi.security import OAuth2PasswordBearer
from pydantic import BaseModel
from jose import JWTError, jwt
from datetime import datetime, timedelta
from typing import Optional
import secrets
from server.database import get_connection
from pathlib import Path
import subprocess
from cryptography.hazmat.primitives.kdf.pbkdf2 import PBKDF2HMAC
from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.backends import default_backend
from cryptography.fernet import Fernet
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
from cryptography.hazmat.primitives import padding
import base64
import logging

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

router = APIRouter()

SECRET_KEY = "supersecretkey"  # REPLACE THIS KEY!!!!!
ALGORITHM = "HS256"
ACCESS_TOKEN_EXPIRE_MINUTES = 60
RECOVERY_TOKEN_EXPIRE_MINUTES = 5

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
    device_part: Optional[str] = None
    qr_part: Optional[str] = None

class RecoveryRequest(BaseModel):
    username: str
    part1: str
    part2: str

class ResetPasswordRequest(BaseModel):
    recovery_token: str
    new_password: str

def hash_password_with_new_salt(password: str) -> str:
    pwd_salm = secrets.token_bytes(16)
    kdf = PBKDF2HMAC(
        algorithm=hashes.SHA256(),
        length=32,
        salt=pwd_salm,
        iterations=100000,
        backend=default_backend()
    )
    hashed_password = kdf.derive(password.encode())
    pwd_salm_b64 = base64.b64encode(pwd_salm).decode()
    hashed_password_b64 = base64.b64encode(hashed_password).decode()
    return f"{pwd_salm_b64}:{hashed_password_b64}"

def verify_password(stored_password: str, provided_password: str) -> bool:
    try:
        pwd_salm_b64, hashed_pwd_b64 = stored_password.split(':')
        pwd_salm = base64.b64decode(pwd_salm_b64)
        stored_hash = base64.b64decode(hashed_pwd_b64)
    except:
        return False
    kdf = PBKDF2HMAC(
        algorithm=hashes.SHA256(),
        length=32,
        salt=pwd_salm,
        iterations=100000,
        backend=default_backend()
    )
    computed_hash = kdf.derive(provided_password.encode())
    return computed_hash == stored_hash

def create_access_token(user_id: int):
    expires_delta = timedelta(minutes=ACCESS_TOKEN_EXPIRE_MINUTES)
    payload = {
        "sub": str(user_id),
        "exp": datetime.utcnow() + expires_delta
    }
    token = jwt.encode(payload, SECRET_KEY, algorithm=ALGORITHM)
    return token

def create_recovery_token(user_id: int):
    expires_delta = timedelta(minutes=RECOVERY_TOKEN_EXPIRE_MINUTES)
    payload = {
        "sub": str(user_id),
        "type": "recovery",
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

def verify_recovery_token(token: str):
    try:
        payload = jwt.decode(token, SECRET_KEY, algorithms=[ALGORITHM])
        if payload.get("type") != "recovery":
            return None
        user_id = payload.get("sub")
        if user_id is None:
            return None
        conn = get_connection()
        cursor = conn.cursor()
        cursor.execute("SELECT id, username FROM users WHERE id = ?", (user_id,))
        user = cursor.fetchone()
        conn.close()
        if user:
            return {"id": user[0], "username": user[1]}
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

def split_master_key(master_key_hex: str, shares: int = 3, threshold: int = 2):
    try:
        result = subprocess.run(
            ['ssss-split', '-t', str(threshold), '-n', str(shares), '-w', master_key_hex],
            input=master_key_hex + '\n', text=True, capture_output=True
        )
        if result.returncode != 0:
            raise Exception(f"ssss-split failed: {result.stderr}")
        shares_output = result.stdout.splitlines()[1:]
        return [share.strip() for share in shares_output]
    except Exception as e:
        raise Exception(f"Error splitting master key: {e}")

def combine_master_key(shares: list[str]):
    try:
        logger.info(f"Combining shares: {shares}")
        shares_input = '\n'.join(shares) + '\n'
        result = subprocess.run(
            ['ssss-combine', '-t', '2'],
            input=shares_input, text=True, capture_output=True
        )
        logger.info(f"ssss-combine stdout: {result.stdout}")
        logger.info(f"ssss-combine stderr: {result.stderr}")
        if result.returncode != 0:
            raise Exception(f"ssss-combine failed: {result.stderr}")
        output = result.stdout + result.stderr
        for line in output.splitlines():
            if "Resulting secret: " in line:
                master_key_hex = line.split("Resulting secret: ")[1].strip()
                logger.info(f"Recovered master_key_hex: {master_key_hex}")
                return master_key_hex
        raise Exception("Could not find master key in ssss-combine output")
    except Exception as e:
        logger.error(f"Error combining master key: {str(e)}")
        raise Exception(f"Error combining master key: {e}")

@router.post("/register", response_model=Token)
def register(user: User):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        master_key = secrets.token_bytes(32)
        master_key_hex = master_key.hex()
        shares = split_master_key(master_key_hex)
        logger.info(f"Generated shares: {shares}")
        device_part = shares[0]
        cloud_part = shares[1]
        qr_part = shares[2]
        cloud_part_plain = cloud_part
        salt = secrets.token_bytes(16)
        padder = padding.PKCS7(128).padder()
        padded_data = padder.update(user.username.encode()) + padder.finalize()
        iv = secrets.token_bytes(16)
        cipher = Cipher(algorithms.AES(master_key), modes.CBC(iv), backend=default_backend())
        encryptor = cipher.encryptor()
        ciphertext = encryptor.update(padded_data) + encryptor.finalize()
        verification_ciphertext = base64.b64encode(iv + ciphertext).decode()
        password_field = hash_password_with_new_salt(user.password)
        cursor.execute(
            "INSERT INTO users (username, password, bio, encrypted_cloud_part, salt, verification_ciphertext) VALUES (?, ?, ?, ?, ?, ?)",
            (user.username, password_field, user.bio or "", cloud_part_plain, salt, verification_ciphertext)
        )
        conn.commit()
        user_id = cursor.lastrowid
    except sqlite3.IntegrityError:
        conn.close()
        raise HTTPException(status_code=400, detail="User already exists")
    except Exception as e:
        conn.close()
        raise HTTPException(status_code=500, detail=f"Error during registration: {str(e)}")
    finally:
        conn.close()
    token = create_access_token(user_id)
    return {
        "access_token": token,
        "token_type": "bearer",
        "device_part": device_part,
        "qr_part": qr_part
    }

@router.post("/login", response_model=Token)
def login(user: User):
    conn = get_connection()
    cursor = conn.cursor()
    cursor.execute("SELECT id, password FROM users WHERE username = ?", (user.username,))
    db_user = cursor.fetchone()
    if not db_user or not verify_password(db_user[1], user.password):
        conn.close()
        raise HTTPException(status_code=401, detail="Incorrect username or password")
    conn.close()
    token = create_access_token(db_user[0])
    return {"access_token": token, "token_type": "bearer"}

@router.get("/me")
async def get_me(current_user: dict = Depends(get_current_user)):
    return {
        "id": current_user["id"],
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
    if update:
        if update.avatar_url is not None:
            updates.append("avatar_url = ?")
            values.append(update.avatar_url)
        if update.bio is not None:
            updates.append("bio = ?")
            values.append(update.bio)
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

@router.post("/me/bio")
async def update_user_bio(bio_data: UserUpdate, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("UPDATE users SET bio = ? WHERE id = ?", (bio_data.bio, current_user["id"]))
        if cursor.rowcount == 0:
            raise HTTPException(status_code=404, detail="User not found")
        conn.commit()
        return {"message": "Bio updated"}
    except Exception as e:
        conn.rollback()
        raise HTTPException(status_code=500, detail=f"Error updating bio: {str(e)}")
    finally:
        conn.close()

@router.get("/users/{username}")
async def get_user_avatar(username: str):
    conn = get_connection()
    cursor = conn.cursor()
    cursor.execute("SELECT avatar_url, bio FROM users WHERE username = ?", (username,))
    user = cursor.fetchone()
    conn.close()
    if not user or not user[0]:
        return {"avatar_url": "/static/avatars/default.jpg", "bio": user[1] if user else ""}
    return {"avatar_url": user[0], "bio": user[1] or ""}

@router.delete("/me")
async def delete_account(current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("DELETE FROM participants WHERE user_id = ?", (current_user["id"],))
        cursor.execute("DELETE FROM users WHERE id = ?", (current_user["id"],))
        conn.commit()
    except Exception as e:
        conn.rollback()
        raise HTTPException(status_code=500, detail=f"Error deleting account: {str(e)}")
    finally:
        conn.close()
    return {"message": "Account deleted"}

@router.post("/recover")
def recover_password(recovery: RecoveryRequest):
    logger.info(f"Recovery request for username: {recovery.username}")
    logger.info(f"Provided part1: {recovery.part1}")
    logger.info(f"Provided part2: {recovery.part2}")
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("SELECT id, encrypted_cloud_part, salt, verification_ciphertext FROM users WHERE username = ?", (recovery.username,))
        user = cursor.fetchone()
        if not user:
            logger.warning(f"User not found: {recovery.username}")
            raise HTTPException(status_code=404, detail="User not found")
        shares = [recovery.part1, recovery.part2]
        try:
            master_key_hex = combine_master_key(shares)
            logger.info(f"Successfully combined master key: {master_key_hex}")
            master_key = bytes.fromhex(master_key_hex)
        except Exception as e:
            logger.error(f"Failed to combine shares: {str(e)}")
            raise HTTPException(status_code=400, detail="Invalid parts provided")
        try:
            verification_data = base64.b64decode(user[3])
            iv = verification_data[:16]
            ciphertext = verification_data[16:]
            cipher = Cipher(algorithms.AES(master_key), modes.CBC(iv), backend=default_backend())
            decryptor = cipher.decryptor()
            padded_plaintext = decryptor.update(ciphertext) + decryptor.finalize()
            unpadder = padding.PKCS7(128).unpadder()
            plaintext = unpadder.update(padded_plaintext) + unpadder.finalize()
            decrypted_username = plaintext.decode()
            logger.info(f"Decrypted username: {decrypted_username}")
        except Exception as e:
            logger.error(f"Decryption error: {str(e)}")
            raise HTTPException(status_code=400, detail="Invalid parts provided")
        if decrypted_username != recovery.username:
            logger.warning(f"Decrypted username mismatch: expected {recovery.username}, got {decrypted_username}")
            raise HTTPException(status_code=400, detail="Invalid parts provided")
        recovery_token = create_recovery_token(user[0])
        logger.info(f"Recovery token generated for user ID {user[0]}")
        return {"message": "Password recovery successful.", "recovery_token": recovery_token}
    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"Unexpected error during recovery: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error during password recovery: {str(e)}")
    finally:
        conn.close()

@router.post("/reset-password")
def reset_password(request: ResetPasswordRequest):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        user = verify_recovery_token(request.recovery_token)
        if not user:
            raise HTTPException(status_code=401, detail="Invalid or expired recovery token")
        password_field = hash_password_with_new_salt(request.new_password)
        cursor.execute("UPDATE users SET password = ? WHERE id = ?", (password_field, user["id"]))
        conn.commit()
        return {"message": "Password reset successful."}
    except Exception as e:
        conn.rollback()
        raise HTTPException(status_code=500, detail=f"Error resetting password: {str(e)}")
    finally:
        conn.close()

@router.get("/get-cloud-part")
async def get_cloud_part(username: str):
    conn = get_connection()
    cursor = conn.cursor()
    logger.info(f"Fetching cloud part for username: {username}")
    cursor.execute("SELECT encrypted_cloud_part FROM users WHERE LOWER(username) = LOWER(?)", (username,))
    row = cursor.fetchone()
    conn.close()
    if not row:
        logger.warning(f"No user found with username: {username}")
        raise HTTPException(status_code=404, detail="User not found")
    if not row[0]:
        logger.warning(f"User found but encrypted_cloud_part is missing for username: {username}")
        raise HTTPException(status_code=404, detail="Cloud part not found")
    logger.info(f"Successfully retrieved encrypted_cloud_part for username: {username}")
    return {"encrypted_cloud_part": row[0]}