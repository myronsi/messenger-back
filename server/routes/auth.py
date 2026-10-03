from fastapi import APIRouter, HTTPException, Depends, status, File, UploadFile, Request, Response, Cookie
from fastapi.security import OAuth2PasswordBearer
from pydantic import BaseModel
from jose import JWTError
from datetime import datetime, timedelta, timezone
from typing import Optional
import os
import re
import secrets
import hashlib
import hmac
import struct
import time
from urllib.parse import quote
from server.database import get_connection
from server import tokens
from server.tokens import FERNET, TOKEN_ACCESS, TOKEN_MEDIA
from pathlib import Path
import subprocess
from cryptography.hazmat.primitives.kdf.pbkdf2 import PBKDF2HMAC
from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.backends import default_backend
from cryptography.fernet import InvalidToken
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
from cryptography.hazmat.primitives import padding
import base64
import logging
from server.presence import is_user_online
from server.time_utils import to_utc_iso, utc_now_iso
from server.avatar_history import record_user_avatar, store_user_avatar
from server.usernames import normalize_username
from server import rate_limit
from server.ws_tickets import tickets as ws_tickets
from server.recovery_shares import decrypt_cloud_part, encrypt_cloud_part
from server.upload_security import AVATAR_MAX_BYTES, process_avatar
from server.privacy import (
    AVATAR_PROFILE_VISIBILITY_SCOPES,
    DEFAULT_PRIVACY_SETTINGS,
    DIRECT_MESSAGE_VISIBILITY_SCOPES,
    ensure_privacy_settings,
    get_privacy_settings,
    GROUP_INVITE_VISIBILITY_SCOPES,
    normalize_visibility,
    PRESENCE_VISIBILITY_SCOPES,
    PRIVACY_EXCEPTION_EFFECTS,
    PRIVACY_EXCEPTION_KEYS,
    serialize_user,
    SEARCH_VISIBILITY,
)

try:
    from argon2 import PasswordHasher
    from argon2.exceptions import VerifyMismatchError, VerificationError
except Exception:
    PasswordHasher = None
    VerifyMismatchError = VerificationError = Exception

logger = logging.getLogger(__name__)

router = APIRouter()

ACCESS_TOKEN_EXPIRE_MINUTES = 15
RECOVERY_TOKEN_EXPIRE_MINUTES = 5
TWO_FACTOR_CHALLENGE_EXPIRE_MINUTES = 5
REFRESH_COOKIE_NAME = "refresh_token"
MEDIA_COOKIE_NAME = "media_session"
REFRESH_COOKIE_PATH = "/auth"
MEDIA_COOKIE_PATH = "/static"

def cookie_path(path: str) -> str:
    """Behind a reverse proxy that mounts the API under a prefix (e.g. /api) browsers only send a
    cookie whose Path includes that prefix, so COOKIE_PATH_PREFIX must match the proxy location."""
    prefix = os.environ.get("COOKIE_PATH_PREFIX", "").strip().rstrip("/")
    if prefix and not prefix.startswith("/"):
        prefix = "/" + prefix
    return prefix + path


def cookie_secure() -> bool:
    return os.environ.get("COOKIE_SECURE", "").strip().lower() in {"1", "true", "yes", "on"}


ALLOWED_SESSION_DURATIONS = {30, 90, 180, 365}
DEFAULT_SESSION_DURATION_DAYS = 90
password_hasher = PasswordHasher() if PasswordHasher else None

oauth2_scheme = OAuth2PasswordBearer(tokenUrl="auth/login")

class RegisterUser(BaseModel):
    username: str
    display_name: str
    password: str
    bio: Optional[str] = None

class LoginUser(BaseModel):
    username: str
    password: str

class TwoFactorLoginRequest(BaseModel):
    login_challenge: str
    code: str

class UserUpdate(BaseModel):
    avatar_url: Optional[str] = None
    bio: Optional[str] = None
    display_name: Optional[str] = None

class PrivacySettingsUpdate(BaseModel):
    avatar_visibility: Optional[str] = None
    profile_visibility: Optional[str] = None
    presence_visibility: Optional[str] = None
    read_receipts_enabled: Optional[bool] = None
    direct_messages: Optional[str] = None
    group_invites: Optional[str] = None
    search_visibility: Optional[str] = None


class PrivacyExceptionsUpdate(BaseModel):
    usernames: list[str]

class Token(BaseModel):
    access_token: Optional[str] = None
    token_type: str
    device_part: Optional[str] = None
    qr_part: Optional[str] = None
    two_factor_required: Optional[bool] = None
    login_challenge: Optional[str] = None

class RecoveryRequest(BaseModel):
    username: str
    part1: str
    part2: Optional[str] = None

class ResetPasswordRequest(BaseModel):
    recovery_token: str
    new_password: str

class PasswordChangeRequest(BaseModel):
    current_password: str
    new_password: str

class SessionDurationUpdate(BaseModel):
    session_duration_days: int

class TwoFactorConfirmRequest(BaseModel):
    code: str

class TwoFactorDisableRequest(BaseModel):
    password: str
    code: str

def normalize_display_name(display_name: str) -> str:
    return " ".join(display_name.strip().split())

def validate_display_name(display_name: str) -> str:
    normalized = normalize_display_name(display_name)
    if len(normalized) < 3 or len(normalized) > 50:
        raise HTTPException(status_code=400, detail="Display name must be between 3 and 50 characters")
    return normalized

def validate_password(password: str):
    if len(password or "") < 8:
        raise HTTPException(status_code=400, detail="Password must be at least 8 characters")

def hash_password_with_salt(password: str) -> str:
    if password_hasher:
        return f"argon2${password_hasher.hash(password)}"
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

_DUMMY_PASSWORD_HASH = None

def _dummy_password_hash() -> str:
    # Lets logins for unknown usernames spend the same hashing time as real ones.
    global _DUMMY_PASSWORD_HASH
    if _DUMMY_PASSWORD_HASH is None:
        _DUMMY_PASSWORD_HASH = hash_password_with_salt(secrets.token_urlsafe(16))
    return _DUMMY_PASSWORD_HASH

def verify_password(stored_password: str, provided_password: str) -> bool:
    if stored_password.startswith("argon2$"):
        if not password_hasher:
            return False
        try:
            return password_hasher.verify(stored_password.removeprefix("argon2$"), provided_password)
        except (VerifyMismatchError, VerificationError, Exception):
            return False
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

def password_needs_rehash(stored_password: str) -> bool:
    if not password_hasher:
        return False
    if not stored_password.startswith("argon2$"):
        return True
    try:
        return password_hasher.check_needs_rehash(stored_password.removeprefix("argon2$"))
    except Exception:
        return True

def utc_now() -> datetime:
    return datetime.now(timezone.utc)

def parse_datetime(value) -> datetime:
    if isinstance(value, datetime):
        return value if value.tzinfo else value.replace(tzinfo=timezone.utc)
    normalized = str(value).replace("Z", "+00:00")
    parsed = datetime.fromisoformat(normalized)
    return parsed if parsed.tzinfo else parsed.replace(tzinfo=timezone.utc)

def require_privacy_visibility(field: str, value: str | None, allowed_values: set[str]) -> str:
    normalized = (value or "").strip().lower()
    if normalized not in allowed_values:
        raise HTTPException(status_code=400, detail=f"{field} does not support this privacy option")
    return normalized

def create_access_token(user_id: int, session_id: str):
    return tokens.create_token(
        TOKEN_ACCESS,
        user_id,
        timedelta(minutes=ACCESS_TOKEN_EXPIRE_MINUTES),
        sid=session_id,
    )

def create_two_factor_challenge(cursor, user_id: int):
    return tokens.issue_two_factor_challenge(
        cursor, user_id, timedelta(minutes=TWO_FACTOR_CHALLENGE_EXPIRE_MINUTES)
    )

def create_recovery_token(cursor, user_id: int):
    return tokens.issue_recovery_token(
        cursor, user_id, timedelta(minutes=RECOVERY_TOKEN_EXPIRE_MINUTES)
    )

def hash_secret(value: str) -> str:
    return hashlib.sha256(value.encode()).hexdigest()

def get_session_duration_days(cursor, user_id: int) -> int:
    cursor.execute("SELECT session_duration_days FROM user_security_settings WHERE user_id = ?", (user_id,))
    row = cursor.fetchone()
    if not row:
        cursor.execute("INSERT OR IGNORE INTO user_security_settings (user_id) VALUES (?)", (user_id,))
        return DEFAULT_SESSION_DURATION_DAYS
    duration = int(row["session_duration_days"] or DEFAULT_SESSION_DURATION_DAYS)
    return duration if duration in ALLOWED_SESSION_DURATIONS else DEFAULT_SESSION_DURATION_DAYS

def create_session(cursor, user_id: int, request: Request):
    session_id = secrets.token_urlsafe(24)
    refresh_token = secrets.token_urlsafe(48)
    duration_days = get_session_duration_days(cursor, user_id)
    expires_at = utc_now() + timedelta(days=duration_days)
    user_agent = request.headers.get("user-agent", "")
    ip_address = request.client.host if request.client else None
    now = utc_now_iso()
    cursor.execute("""
        INSERT INTO user_sessions (
            id, user_id, refresh_token_hash, user_agent, ip_address,
            created_at, last_active_at, expires_at
        )
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)
    """, (
        session_id,
        user_id,
        hash_secret(refresh_token),
        user_agent,
        ip_address,
        now,
        now,
        expires_at.isoformat(),
    ))
    return session_id, refresh_token, expires_at

def set_refresh_cookie(response: Response, refresh_token: str, expires_at: datetime):
    max_age = max(0, int((expires_at - utc_now()).total_seconds()))
    response.set_cookie(
        key=REFRESH_COOKIE_NAME,
        value=refresh_token,
        max_age=max_age,
        httponly=True,
        secure=cookie_secure(),
        samesite="lax",
        path=cookie_path(REFRESH_COOKIE_PATH),
    )

def clear_refresh_cookie(response: Response):
    response.delete_cookie(REFRESH_COOKIE_NAME, path=cookie_path(REFRESH_COOKIE_PATH))
    response.delete_cookie(MEDIA_COOKIE_NAME, path=cookie_path(MEDIA_COOKIE_PATH))

def set_media_cookie(response: Response, user_id: int, session_id: str, expires_at: datetime):
    """Lets <img>/<audio> requests for /static files prove who is asking; the session is re-checked on every request."""
    max_age = max(0, int((expires_at - utc_now()).total_seconds()))
    response.set_cookie(
        key=MEDIA_COOKIE_NAME,
        value=tokens.create_token(TOKEN_MEDIA, user_id, timedelta(seconds=max_age), sid=session_id),
        max_age=max_age,
        httponly=True,
        secure=cookie_secure(),
        samesite="lax",
        path=cookie_path(MEDIA_COOKIE_PATH),
    )

def get_active_session(cursor, user_id: int, session_id: str):
    cursor.execute("""
        SELECT *
        FROM user_sessions
        WHERE id = ? AND user_id = ? AND revoked_at IS NULL
    """, (session_id, user_id))
    session = cursor.fetchone()
    if not session:
        return None
    if parse_datetime(session["expires_at"]) <= utc_now():
        return None
    return session

def get_user_security_settings(cursor, user_id: int):
    cursor.execute("SELECT * FROM user_security_settings WHERE user_id = ?", (user_id,))
    row = cursor.fetchone()
    if not row:
        cursor.execute("INSERT OR IGNORE INTO user_security_settings (user_id) VALUES (?)", (user_id,))
        cursor.execute("SELECT * FROM user_security_settings WHERE user_id = ?", (user_id,))
        row = cursor.fetchone()
    return row

def encrypt_secret(value: str | None) -> str | None:
    if value is None:
        return None
    return FERNET.encrypt(value.encode()).decode()

def decrypt_secret(value: str | None) -> str | None:
    if not value:
        return None
    try:
        return FERNET.decrypt(value.encode()).decode()
    except InvalidToken:
        # Stored under a different key (e.g. after rotation); treat as no usable TOTP secret.
        logger.warning("Stored TOTP secret cannot be decrypted with the current SECRET_KEY")
        return None

def generate_totp_secret() -> str:
    return base64.b32encode(secrets.token_bytes(20)).decode().rstrip("=")

def hotp(secret: str, counter: int, digits: int = 6) -> str:
    padded_secret = secret + "=" * ((8 - len(secret) % 8) % 8)
    key = base64.b32decode(padded_secret, casefold=True)
    msg = struct.pack(">Q", counter)
    digest = hmac.new(key, msg, hashlib.sha1).digest()
    offset = digest[-1] & 0x0F
    code = struct.unpack(">I", digest[offset:offset + 4])[0] & 0x7FFFFFFF
    return str(code % (10 ** digits)).zfill(digits)

def verify_totp(secret: str, code: str) -> bool:
    normalized = "".join(ch for ch in code if ch.isdigit())
    if len(normalized) != 6:
        return False
    counter = int(time.time() // 30)
    for offset in (-1, 0, 1):
        if hmac.compare_digest(hotp(secret, counter + offset), normalized):
            return True
    return False

def make_otpauth_uri(username: str, secret: str) -> str:
    label = quote(f"Messenger:{username}")
    issuer = quote("Messenger")
    return f"otpauth://totp/{label}?secret={secret}&issuer={issuer}&algorithm=SHA1&digits=6&period=30"

def generate_recovery_codes() -> list[str]:
    return [f"{secrets.token_hex(4).upper()}-{secrets.token_hex(4).upper()}" for _ in range(10)]

def store_recovery_codes(cursor, user_id: int, codes: list[str]):
    cursor.execute("DELETE FROM user_2fa_recovery_codes WHERE user_id = ?", (user_id,))
    for code in codes:
        cursor.execute(
            "INSERT INTO user_2fa_recovery_codes (user_id, code_hash, created_at) VALUES (?, ?, ?)",
            (user_id, hash_secret(code.replace("-", "").upper()), utc_now_iso()),
        )

def use_recovery_code(cursor, user_id: int, code: str) -> bool:
    normalized = code.replace("-", "").replace(" ", "").upper()
    if not normalized:
        return False
    code_hash = hash_secret(normalized)
    cursor.execute("""
        SELECT id
        FROM user_2fa_recovery_codes
        WHERE user_id = ? AND code_hash = ? AND used_at IS NULL
    """, (user_id, code_hash))
    row = cursor.fetchone()
    if not row:
        return False
    cursor.execute("UPDATE user_2fa_recovery_codes SET used_at = ? WHERE id = ?", (utc_now_iso(), row["id"]))
    return True

def verify_two_factor_or_recovery(cursor, user_id: int, code: str) -> bool:
    settings = get_user_security_settings(cursor, user_id)
    secret = decrypt_secret(settings["two_factor_secret"])
    if secret and verify_totp(secret, code):
        return True
    return use_recovery_code(cursor, user_id, code)

def _load_session_user(user_id, session_id):
    conn = get_connection()
    try:
        cursor = conn.cursor()
        if not get_active_session(cursor, int(user_id), session_id):
            return None
        cursor.execute("UPDATE user_sessions SET last_active_at = ? WHERE id = ?", (utc_now_iso(), session_id))
        conn.commit()
        cursor.execute("SELECT id, username, display_name, avatar_url, bio, created_at, last_seen FROM users WHERE id = ?", (user_id,))
        user = cursor.fetchone()
    finally:
        conn.close()
    if not user:
        return None
    return {
        "id": user[0],
        "username": user[1],
        "display_name": user[2],
        "avatar_url": user[3],
        "bio": user[4],
        "created_at": to_utc_iso(user[5]),
        "last_seen": to_utc_iso(user[6]),
        "is_online": is_user_online(user[0]),
        "session_id": session_id,
    }

def verify_token(token: str):
    try:
        payload = tokens.decode_token(token, TOKEN_ACCESS)
    except JWTError:
        return None
    user_id = payload.get("sub")
    session_id = payload.get("sid")
    if user_id is None or session_id is None:
        return None
    return _load_session_user(user_id, session_id)

def verify_ws_ticket(ticket: str):
    """Redeem a one-time WebSocket ticket; the session it was issued for must still be active."""
    claim = ws_tickets.consume(ticket)
    if not claim:
        return None
    return _load_session_user(*claim)

def get_user_by_id(user_id: int):
    conn = get_connection()
    cursor = conn.cursor()
    cursor.execute("SELECT id, username, display_name, avatar_url, bio, created_at, last_seen FROM users WHERE id = ?", (user_id,))
    row = cursor.fetchone()
    conn.close()
    if row:
        return {
            "id": row[0],
            "username": row[1],
            "display_name": row[2],
            "avatar_url": row[3],
            "bio": row[4],
            "created_at": to_utc_iso(row[5]),
            "last_seen": to_utc_iso(row[6]),
            "is_online": is_user_online(row[0])
        }
    return None

async def get_current_user(token: str = Depends(oauth2_scheme)):
    try:
        payload = tokens.decode_token(token, TOKEN_ACCESS)
        user_id = int(payload.get("sub"))
        session_id = payload.get("sid")
        if not session_id:
            raise HTTPException(status_code=401, detail="Invalid token")
        conn = get_connection()
        cursor = conn.cursor()
        if not get_active_session(cursor, user_id, session_id):
            conn.close()
            raise HTTPException(status_code=401, detail="Invalid session")
        cursor.execute("UPDATE user_sessions SET last_active_at = ? WHERE id = ?", (utc_now_iso(), session_id))
        conn.commit()
        conn.close()
        user = get_user_by_id(user_id)
        if not user:
            raise HTTPException(status_code=401, detail="Invalid token")
        user["session_id"] = session_id
        return user
    except (JWTError, ValueError):
        raise HTTPException(status_code=401, detail="Invalid token")

def split_master_key(master_key_hex: str, shares: int = 3, threshold: int = 2):
    try:
        result = subprocess.run(
            ['ssss-split', '-t', str(threshold), '-n', str(shares), '-w', master_key_hex],
            input=master_key_hex + '\n', text=True, capture_output=True
        )
        if result.returncode != 0:
            raise Exception(f"ssss-split failed: {result.stderr}")
        all_lines = result.stdout.splitlines()
        # Filter out empty lines and header lines (lines that don't start with the key prefix or contain '-N-')
        shares_output = [line.strip() for line in all_lines if line.strip() and '-' in line]
        return shares_output
    except Exception as e:
        raise Exception(f"Error splitting master key: {e}")

def combine_master_key(shares: list[str]):
    try:
        shares_input = '\n'.join(shares) + '\n'
        result = subprocess.run(
            ['ssss-combine', '-t', '2'],
            input=shares_input, text=True, capture_output=True
        )
        if result.returncode != 0:
            raise Exception(f"ssss-combine failed: {result.stderr}")
        output = result.stdout + result.stderr
        for line in output.splitlines():
            if "Resulting secret: " in line:
                master_key_hex = line.split("Resulting secret: ")[1].strip()
                return master_key_hex
        raise Exception("Could not find master key in ssss-combine output")
    except Exception as e:
        logger.error("Error combining master key: %s", type(e).__name__)
        raise Exception(f"Error combining master key: {e}")

@router.post("/register", response_model=Token)
def register(user: RegisterUser, request: Request, response: Response):
    username = normalize_username(user.username)
    display_name = validate_display_name(user.display_name)
    validate_password(user.password)
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("SELECT 1 FROM users WHERE LOWER(username) = LOWER(?)", (username,))
        if cursor.fetchone():
            raise HTTPException(status_code=400, detail="User already exists")
        master_key = secrets.token_bytes(32)
        master_key_hex = master_key.hex()
        shares = split_master_key(master_key_hex)
        if not isinstance(shares, list) or len(shares) < 3:
            logger.error("Insufficient shares returned by split_master_key")
            raise Exception(f"Insufficient shares returned by split_master_key: expected 3, got {len(shares) if hasattr(shares, '__len__') else 'unknown'}")
        device_part = shares[0]
        cloud_part = shares[1]
        qr_part = shares[2]
        cloud_part_stored = encrypt_cloud_part(cloud_part)
        salt = secrets.token_bytes(16)
        padder = padding.PKCS7(128).padder()
        padded_data = padder.update(username.encode()) + padder.finalize()
        iv = secrets.token_bytes(16)
        cipher = Cipher(algorithms.AES(master_key), modes.CBC(iv), backend=default_backend())
        encryptor = cipher.encryptor()
        ciphertext = encryptor.update(padded_data) + encryptor.finalize()
        verification_ciphertext = base64.b64encode(iv + ciphertext).decode()
        password_field = hash_password_with_salt(user.password)
        now = utc_now_iso()
        cursor.execute(
            "INSERT INTO users (username, display_name, password, bio, created_at, last_seen, encrypted_cloud_part, salt, verification_ciphertext) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
            (username, display_name, password_field, user.bio or "", now, now, cloud_part_stored, salt, verification_ciphertext)
        )
        user_id = cursor.lastrowid
        if user_id is None:
            conn.rollback()
            raise HTTPException(status_code=400, detail="User already exists")
        conn.commit()
        ensure_privacy_settings(cursor, user_id)
        cursor.execute("INSERT OR IGNORE INTO user_security_settings (user_id) VALUES (?)", (user_id,))
        session_id, refresh_token, expires_at = create_session(cursor, user_id, request)
        conn.commit()
        set_refresh_cookie(response, refresh_token, expires_at)
        set_media_cookie(response, user_id, session_id, expires_at)
    except HTTPException:
        raise
    except sqlite3.IntegrityError:
        conn.close()
        raise HTTPException(status_code=400, detail="User already exists")
    except Exception as e:
        logger.exception("Error during registration")
        conn.close()
        raise HTTPException(status_code=500, detail=f"Error during registration: {str(e)}")
    finally:
        conn.close()
    token = create_access_token(user_id, session_id)
    return {
        "access_token": token,
        "token_type": "bearer",
        "device_part": device_part,
        "qr_part": qr_part
    }

@router.post("/login", response_model=Token)
def login(user: LoginUser, request: Request, response: Response):
    ip_key = rate_limit.client_ip(request)
    user_key = user.username.strip().lower()
    rate_limit.enforce(rate_limit.LOGIN_IP, ip_key)
    rate_limit.enforce(rate_limit.LOGIN_USER, user_key)
    conn = get_connection()
    cursor = conn.cursor()
    cursor.execute(
        "SELECT id, password FROM users WHERE LOWER(username) = LOWER(?) ORDER BY (username = ?) DESC LIMIT 1",
        (user.username, user.username),
    )
    db_user = cursor.fetchone()
    stored_hash = db_user[1] if db_user else _dummy_password_hash()
    password_ok = verify_password(stored_hash, user.password)
    if not db_user or not password_ok:
        conn.close()
        rate_limit.LOGIN_IP.hit(ip_key)
        rate_limit.LOGIN_USER.hit(user_key)
        raise HTTPException(status_code=401, detail="Incorrect username or password")
    rate_limit.LOGIN_USER.reset(user_key)
    if password_needs_rehash(db_user["password"]):
        cursor.execute("UPDATE users SET password = ? WHERE id = ?", (hash_password_with_salt(user.password), db_user["id"]))
        conn.commit()
    settings = get_user_security_settings(cursor, db_user["id"])
    if settings["two_factor_enabled"]:
        login_challenge = create_two_factor_challenge(cursor, db_user["id"])
        conn.commit()
        conn.close()
        return {
            "access_token": None,
            "token_type": "bearer",
            "two_factor_required": True,
            "login_challenge": login_challenge,
        }
    session_id, refresh_token, expires_at = create_session(cursor, db_user["id"], request)
    conn.commit()
    conn.close()
    set_refresh_cookie(response, refresh_token, expires_at)
    set_media_cookie(response, db_user["id"], session_id, expires_at)
    token = create_access_token(db_user["id"], session_id)
    return {"access_token": token, "token_type": "bearer"}

@router.post("/login/2fa", response_model=Token)
def login_two_factor(payload: TwoFactorLoginRequest, request: Request, response: Response):
    ip_key = rate_limit.client_ip(request)
    rate_limit.enforce(rate_limit.TWO_FACTOR_IP, ip_key)
    conn = get_connection()
    cursor = conn.cursor()
    try:
        attempt = tokens.begin_two_factor_attempt(cursor, payload.login_challenge)
        # Persist the attempt before verifying so failed guesses always count towards the limit.
        conn.commit()
        if not attempt:
            rate_limit.TWO_FACTOR_IP.hit(ip_key)
            raise HTTPException(status_code=401, detail="Invalid or expired login challenge")
        user_id, challenge_hash = attempt
        user_key = str(user_id)
        rate_limit.enforce(rate_limit.TWO_FACTOR_USER, user_key)
        if not verify_two_factor_or_recovery(cursor, user_id, payload.code):
            conn.rollback()
            rate_limit.TWO_FACTOR_IP.hit(ip_key)
            rate_limit.TWO_FACTOR_USER.hit(user_key)
            raise HTTPException(status_code=401, detail="Invalid verification code")
        rate_limit.TWO_FACTOR_USER.reset(user_key)
        tokens.finish_two_factor_challenge(cursor, challenge_hash)
        session_id, refresh_token, expires_at = create_session(cursor, user_id, request)
        conn.commit()
        set_refresh_cookie(response, refresh_token, expires_at)
        set_media_cookie(response, user_id, session_id, expires_at)
        return {"access_token": create_access_token(user_id, session_id), "token_type": "bearer"}
    except HTTPException:
        raise
    finally:
        conn.close()

@router.get("/me")
async def get_me(current_user: dict = Depends(get_current_user)):
    return {
        "id": current_user["id"],
        "username": current_user["username"],
        "display_name": current_user["display_name"],
        "avatar_url": current_user["avatar_url"],
        "bio": current_user["bio"],
        "created_at": current_user.get("created_at"),
        "is_online": current_user["is_online"],
        "last_seen": to_utc_iso(current_user["last_seen"])
    }

@router.post("/ws-ticket")
async def create_ws_ticket(current_user: dict = Depends(get_current_user)):
    ticket = ws_tickets.issue(current_user["id"], current_user["session_id"])
    if ticket is None:
        raise HTTPException(status_code=429, detail="Too many pending WebSocket tickets")
    return {"ticket": ticket, "expires_in": 30}

@router.post("/refresh", response_model=Token)
async def refresh_access_token(
    request: Request,
    response: Response,
    refresh_token: str | None = Cookie(default=None, alias=REFRESH_COOKIE_NAME),
):
    if not refresh_token:
        raise HTTPException(status_code=401, detail="Missing refresh token")

    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("""
            SELECT *
            FROM user_sessions
            WHERE refresh_token_hash = ? AND revoked_at IS NULL
        """, (hash_secret(refresh_token),))
        session = cursor.fetchone()
        if not session or parse_datetime(session["expires_at"]) <= utc_now():
            clear_refresh_cookie(response)
            raise HTTPException(status_code=401, detail="Invalid or expired session")

        new_refresh_token = secrets.token_urlsafe(48)
        expires_at = utc_now() + timedelta(days=get_session_duration_days(cursor, session["user_id"]))
        cursor.execute("""
            UPDATE user_sessions
            SET refresh_token_hash = ?, last_active_at = ?, expires_at = ?, user_agent = ?, ip_address = ?
            WHERE id = ?
        """, (
            hash_secret(new_refresh_token),
            utc_now_iso(),
            expires_at.isoformat(),
            request.headers.get("user-agent", ""),
            request.client.host if request.client else None,
            session["id"],
        ))
        conn.commit()
        set_refresh_cookie(response, new_refresh_token, expires_at)
        set_media_cookie(response, session["user_id"], session["id"], expires_at)
        return {"access_token": create_access_token(session["user_id"], session["id"]), "token_type": "bearer"}
    finally:
        conn.close()

@router.post("/logout")
async def logout(response: Response, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute(
            "UPDATE user_sessions SET revoked_at = ? WHERE id = ? AND user_id = ?",
            (utc_now_iso(), current_user["session_id"], current_user["id"]),
        )
        conn.commit()
        clear_refresh_cookie(response)
    finally:
        conn.close()
    from server.websocket import manager
    await manager.close_user_sockets(current_user["id"], session_id=current_user["session_id"])
    return {"message": "Logged out"}

@router.get("/me/security")
async def get_security_settings(current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        settings = get_user_security_settings(cursor, current_user["id"])
        cursor.execute("""
            SELECT COUNT(*) AS remaining
            FROM user_2fa_recovery_codes
            WHERE user_id = ? AND used_at IS NULL
        """, (current_user["id"],))
        recovery = cursor.fetchone()
        conn.commit()
        return {
            "session_duration_days": int(settings["session_duration_days"] or DEFAULT_SESSION_DURATION_DAYS),
            "two_factor_enabled": bool(settings["two_factor_enabled"]),
            "recovery_codes_remaining": recovery["remaining"] if recovery else 0,
        }
    finally:
        conn.close()

@router.put("/me/security/session-duration")
async def update_session_duration(payload: SessionDurationUpdate, current_user: dict = Depends(get_current_user)):
    if payload.session_duration_days not in ALLOWED_SESSION_DURATIONS:
        raise HTTPException(status_code=400, detail="Session duration must be 30, 90, 180, or 365 days")
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("""
            INSERT INTO user_security_settings (user_id, session_duration_days, updated_at)
            VALUES (?, ?, ?)
            ON CONFLICT(user_id)
            DO UPDATE SET session_duration_days = excluded.session_duration_days, updated_at = excluded.updated_at
        """, (current_user["id"], payload.session_duration_days, utc_now_iso()))
        conn.commit()
        return await get_security_settings(current_user)
    finally:
        conn.close()

@router.get("/me/sessions")
async def list_sessions(current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("""
            SELECT id, user_agent, ip_address, created_at, last_active_at, expires_at
            FROM user_sessions
            WHERE user_id = ? AND revoked_at IS NULL AND expires_at > ?
            ORDER BY last_active_at DESC
        """, (current_user["id"], utc_now().isoformat()))
        return {
            "sessions": [
                {
                    "id": row["id"],
                    "user_agent": row["user_agent"] or "",
                    "ip_address": row["ip_address"],
                    "created_at": to_utc_iso(row["created_at"]),
                    "last_active_at": to_utc_iso(row["last_active_at"]),
                    "expires_at": to_utc_iso(row["expires_at"]),
                    "is_current": row["id"] == current_user["session_id"],
                }
                for row in cursor.fetchall()
            ]
        }
    finally:
        conn.close()

@router.delete("/me/sessions/others")
async def revoke_other_sessions(current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("""
            UPDATE user_sessions
            SET revoked_at = ?
            WHERE user_id = ? AND id != ? AND revoked_at IS NULL
        """, (utc_now_iso(), current_user["id"], current_user["session_id"]))
        conn.commit()
    finally:
        conn.close()
    from server.websocket import manager
    await manager.close_user_sockets(current_user["id"], except_session_id=current_user["session_id"])
    return {"message": "Other sessions revoked"}

@router.delete("/me/sessions/{session_id}")
async def revoke_session(session_id: str, response: Response, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("""
            UPDATE user_sessions
            SET revoked_at = ?
            WHERE user_id = ? AND id = ? AND revoked_at IS NULL
        """, (utc_now_iso(), current_user["id"], session_id))
        conn.commit()
        if session_id == current_user["session_id"]:
            clear_refresh_cookie(response)
    finally:
        conn.close()
    from server.websocket import manager
    await manager.close_user_sockets(current_user["id"], session_id=session_id)
    return {"message": "Session revoked"}

@router.post("/me/password")
async def change_password(payload: PasswordChangeRequest, current_user: dict = Depends(get_current_user)):
    validate_password(payload.new_password)
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("SELECT password FROM users WHERE id = ?", (current_user["id"],))
        row = cursor.fetchone()
        if not row or not verify_password(row["password"], payload.current_password):
            raise HTTPException(status_code=400, detail="Current password is incorrect")
        cursor.execute("UPDATE users SET password = ? WHERE id = ?", (hash_password_with_salt(payload.new_password), current_user["id"]))
        cursor.execute("""
            UPDATE user_sessions
            SET revoked_at = ?
            WHERE user_id = ? AND id != ? AND revoked_at IS NULL
        """, (utc_now_iso(), current_user["id"], current_user["session_id"]))
        conn.commit()
    except HTTPException:
        raise
    finally:
        conn.close()
    from server.websocket import manager
    await manager.close_user_sockets(current_user["id"], except_session_id=current_user["session_id"])
    return {"message": "Password changed"}

@router.post("/me/2fa/setup")
async def setup_two_factor(current_user: dict = Depends(get_current_user)):
    secret = generate_totp_secret()
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("""
            UPDATE user_security_settings
            SET pending_two_factor_secret = ?, updated_at = ?
            WHERE user_id = ?
        """, (encrypt_secret(secret), utc_now_iso(), current_user["id"]))
        if cursor.rowcount == 0:
            cursor.execute("""
                INSERT INTO user_security_settings (user_id, pending_two_factor_secret, updated_at)
                VALUES (?, ?, ?)
            """, (current_user["id"], encrypt_secret(secret), utc_now_iso()))
        conn.commit()
        return {
            "secret": secret,
            "otpauth_uri": make_otpauth_uri(current_user["username"], secret),
        }
    finally:
        conn.close()

@router.post("/me/2fa/confirm")
async def confirm_two_factor(payload: TwoFactorConfirmRequest, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        settings = get_user_security_settings(cursor, current_user["id"])
        secret = decrypt_secret(settings["pending_two_factor_secret"])
        if not secret:
            raise HTTPException(status_code=400, detail="Two-factor setup has not been started")
        if not verify_totp(secret, payload.code):
            raise HTTPException(status_code=400, detail="Invalid verification code")
        recovery_codes = generate_recovery_codes()
        store_recovery_codes(cursor, current_user["id"], recovery_codes)
        cursor.execute("""
            UPDATE user_security_settings
            SET two_factor_enabled = 1,
                two_factor_secret = ?,
                pending_two_factor_secret = NULL,
                updated_at = ?
            WHERE user_id = ?
        """, (encrypt_secret(secret), utc_now_iso(), current_user["id"]))
        conn.commit()
        return {"message": "Two-factor authentication enabled", "recovery_codes": recovery_codes}
    except HTTPException:
        raise
    finally:
        conn.close()

@router.post("/me/2fa/disable")
async def disable_two_factor(payload: TwoFactorDisableRequest, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("SELECT password FROM users WHERE id = ?", (current_user["id"],))
        row = cursor.fetchone()
        if not row or not verify_password(row["password"], payload.password):
            raise HTTPException(status_code=400, detail="Password is incorrect")
        if not verify_two_factor_or_recovery(cursor, current_user["id"], payload.code):
            raise HTTPException(status_code=400, detail="Invalid verification code")
        cursor.execute("""
            UPDATE user_security_settings
            SET two_factor_enabled = 0,
                two_factor_secret = NULL,
                pending_two_factor_secret = NULL,
                updated_at = ?
            WHERE user_id = ?
        """, (utc_now_iso(), current_user["id"]))
        cursor.execute("DELETE FROM user_2fa_recovery_codes WHERE user_id = ?", (current_user["id"],))
        conn.commit()
        return {"message": "Two-factor authentication disabled"}
    except HTTPException:
        raise
    finally:
        conn.close()

@router.get("/me/privacy")
async def get_my_privacy(current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        settings = get_privacy_settings(cursor, current_user["id"], include_exceptions=True)
        conn.commit()
        return settings
    finally:
        conn.close()

@router.put("/me/privacy")
async def update_my_privacy(payload: PrivacySettingsUpdate, current_user: dict = Depends(get_current_user)):
    updates = []
    values = []
    data = payload.dict(exclude_unset=True)

    if "avatar_visibility" in data:
        value = require_privacy_visibility("avatar_visibility", data["avatar_visibility"], AVATAR_PROFILE_VISIBILITY_SCOPES)
        updates.append("avatar_visibility = ?")
        values.append(value)

    if "profile_visibility" in data:
        value = require_privacy_visibility("profile_visibility", data["profile_visibility"], AVATAR_PROFILE_VISIBILITY_SCOPES)
        updates.append("profile_visibility = ?")
        values.append(value)

    if "presence_visibility" in data:
        value = require_privacy_visibility("presence_visibility", data["presence_visibility"], PRESENCE_VISIBILITY_SCOPES)
        updates.append("presence_visibility = ?")
        values.append(value)

    if "group_invites" in data:
        value = require_privacy_visibility("group_invites", data["group_invites"], GROUP_INVITE_VISIBILITY_SCOPES)
        updates.append("group_invites = ?")
        values.append(value)

    if "direct_messages" in data:
        value = require_privacy_visibility("direct_messages", data["direct_messages"], DIRECT_MESSAGE_VISIBILITY_SCOPES)
        updates.append("direct_messages = ?")
        values.append(value)

    if "search_visibility" in data:
        value = normalize_visibility(data["search_visibility"], SEARCH_VISIBILITY)
        updates.append("search_visibility = ?")
        values.append(value)

    if "read_receipts_enabled" in data:
        updates.append("read_receipts_enabled = ?")
        values.append(data["read_receipts_enabled"])

    conn = get_connection()
    cursor = conn.cursor()
    try:
        get_privacy_settings(cursor, current_user["id"])
        if updates:
            values.append(current_user["id"])
            cursor.execute(f"UPDATE user_privacy_settings SET {', '.join(updates)} WHERE user_id = ?", values)
        conn.commit()
        return get_privacy_settings(cursor, current_user["id"], include_exceptions=True)
    finally:
        conn.close()


@router.put("/me/privacy/exceptions/{setting_key}/{effect}")
async def update_my_privacy_exceptions(
    setting_key: str,
    effect: str,
    payload: PrivacyExceptionsUpdate,
    current_user: dict = Depends(get_current_user),
):
    normalized_key = setting_key.strip().lower()
    normalized_effect = effect.strip().lower()
    if normalized_key not in PRIVACY_EXCEPTION_KEYS:
        raise HTTPException(status_code=400, detail="Unsupported privacy setting")
    if normalized_effect not in PRIVACY_EXCEPTION_EFFECTS:
        raise HTTPException(status_code=400, detail="Unsupported exception effect")

    normalized_usernames = []
    seen_usernames = set()
    for username in payload.usernames:
        normalized_username = username.strip()
        username_key = normalized_username.lower()
        if not normalized_username or username_key in seen_usernames:
            continue
        seen_usernames.add(username_key)
        normalized_usernames.append(normalized_username)

    conn = get_connection()
    cursor = conn.cursor()
    try:
        target_ids = []
        for username in normalized_usernames:
            cursor.execute("SELECT id, username FROM users WHERE username = ?", (username,))
            target = cursor.fetchone()
            if not target:
                raise HTTPException(status_code=404, detail=f"User {username} not found")
            if target["id"] == current_user["id"]:
                raise HTTPException(status_code=400, detail="You cannot add yourself to privacy exceptions")
            target_ids.append(target["id"])

        cursor.execute(
            """
            DELETE FROM user_privacy_exceptions
            WHERE owner_id = ? AND setting_key = ? AND effect = ?
            """,
            (current_user["id"], normalized_key, normalized_effect),
        )
        for target_id in target_ids:
            cursor.execute(
                """
                DELETE FROM user_privacy_exceptions
                WHERE owner_id = ? AND setting_key = ? AND target_user_id = ?
                """,
                (current_user["id"], normalized_key, target_id),
            )
            cursor.execute(
                """
                INSERT INTO user_privacy_exceptions (owner_id, setting_key, target_user_id, effect, created_at)
                VALUES (?, ?, ?, ?, ?)
                """,
                (current_user["id"], normalized_key, target_id, normalized_effect, utc_now_iso()),
            )
        conn.commit()
        return get_privacy_settings(cursor, current_user["id"], include_exceptions=True)
    except HTTPException:
        conn.rollback()
        raise
    except Exception:
        conn.rollback()
        raise
    finally:
        conn.close()


@router.get("/me/blocked-users")
async def get_blocked_users(current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("""
            SELECT u.id, u.username, u.display_name, u.avatar_url, u.bio, u.created_at, u.last_seen, b.created_at AS blocked_at
            FROM user_blocks b
            JOIN users u ON u.id = b.blocked_id
            WHERE b.blocker_id = ?
            ORDER BY u.username COLLATE NOCASE
        """, (current_user["id"],))
        users = []
        for row in cursor.fetchall():
            user = serialize_user(cursor, row, current_user["id"])
            user["blocked_at"] = to_utc_iso(row["blocked_at"])
            users.append(user)
        return {"users": users}
    finally:
        conn.close()

@router.post("/me/blocked-users/{username}")
async def block_user(username: str, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("SELECT id FROM users WHERE username = ?", (username,))
        target = cursor.fetchone()
        if not target:
            raise HTTPException(status_code=404, detail="User not found")
        if target["id"] == current_user["id"]:
            raise HTTPException(status_code=400, detail="You cannot block yourself")
        cursor.execute(
            "INSERT OR IGNORE INTO user_blocks (blocker_id, blocked_id, created_at) VALUES (?, ?, ?)",
            (current_user["id"], target["id"], utc_now_iso()),
        )
        conn.commit()
        return {"message": "User blocked"}
    finally:
        conn.close()

@router.delete("/me/blocked-users/{username}")
async def unblock_user(username: str, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("SELECT id FROM users WHERE username = ?", (username,))
        target = cursor.fetchone()
        if not target:
            raise HTTPException(status_code=404, detail="User not found")
        cursor.execute(
            "DELETE FROM user_blocks WHERE blocker_id = ? AND blocked_id = ?",
            (current_user["id"], target["id"]),
        )
        conn.commit()
        return {"message": "User unblocked"}
    finally:
        conn.close()

@router.put("/me")
async def update_user_profile(update: UserUpdate = None, current_user: dict = Depends(get_current_user)):
    updates = []
    values = []
    display_name = None
    if update:
        if update.avatar_url is not None:
            updates.append("avatar_url = ?")
            values.append(update.avatar_url)
        if update.bio is not None:
            updates.append("bio = ?")
            values.append(update.bio)
        if update.display_name is not None:
            display_name = validate_display_name(update.display_name)
            updates.append("display_name = ?")
            values.append(display_name)
    conn = get_connection()
    cursor = conn.cursor()
    if updates:
        query = f"UPDATE users SET {', '.join(updates)} WHERE id = ?"
        values.append(current_user["id"])
        cursor.execute(query, values)
        if update and update.display_name is not None:
            cursor.execute(
                "UPDATE messages SET sender_name = ? WHERE sender_id = ?",
                (display_name, current_user["id"])
            )
        conn.commit()
        cursor.execute("SELECT id, username, display_name, avatar_url, bio, created_at, last_seen FROM users WHERE id = ?", (current_user["id"],))
        row = cursor.fetchone()
        conn.close()
        return {
            "id": row["id"],
            "username": row["username"],
            "display_name": row["display_name"],
            "avatar_url": row["avatar_url"],
            "bio": row["bio"],
            "created_at": to_utc_iso(row["created_at"]),
            "is_online": is_user_online(row["id"]),
            "last_seen": to_utc_iso(row["last_seen"])
        }
    conn.close()
    return {"message": "Profile updated" if updates else "No updates provided"}

@router.post("/me/avatar")
async def upload_avatar(file: UploadFile = File(...), current_user: dict = Depends(get_current_user)):
    content = await file.read(AVATAR_MAX_BYTES + 1)
    image_bytes, extension = process_avatar(content)

    avatar_url = store_user_avatar(current_user["id"], image_bytes, extension)
    record_user_avatar(current_user["id"], avatar_url)
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
async def get_user_avatar(username: str, current_user: dict = Depends(get_current_user)):
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("SELECT id, username, display_name, avatar_url, bio, created_at, last_seen FROM users WHERE username = ?", (username,))
        user = cursor.fetchone()
        if not user:
            raise HTTPException(status_code=404, detail="User not found")
        return serialize_user(cursor, user, current_user["id"])
    finally:
        conn.close()

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
    from server.websocket import manager
    await manager.close_user_sockets(current_user["id"])
    return {"message": "Account deleted"}

SHARE_PATTERN = re.compile(r"^[0-9]{1,3}-[0-9]{1,3}-[0-9a-fA-F]{1,128}$")
RECOVERY_FAILED_DETAIL = "Recovery failed. Check the username and the recovery part."

def _recovery_failed() -> HTTPException:
    return HTTPException(status_code=400, detail=RECOVERY_FAILED_DETAIL)

@router.post("/recover")
def recover_password(recovery: RecoveryRequest, request: Request):
    ip_key = rate_limit.client_ip(request)
    user_key = recovery.username.strip().lower()
    rate_limit.enforce(rate_limit.RECOVER_IP, ip_key)
    rate_limit.enforce(rate_limit.RECOVER_USER, user_key)
    rate_limit.RECOVER_IP.hit(ip_key)
    rate_limit.RECOVER_USER.hit(user_key)
    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute(
            "SELECT id, encrypted_cloud_part, salt, verification_ciphertext, username FROM users "
            "WHERE LOWER(username) = LOWER(?) ORDER BY (username = ?) DESC LIMIT 1",
            (recovery.username, recovery.username),
        )
        user = cursor.fetchone()
        part1 = recovery.part1.strip()
        part2 = recovery.part2.strip() if recovery.part2 else None
        if not SHARE_PATTERN.match(part1) or (part2 is not None and not SHARE_PATTERN.match(part2)):
            raise _recovery_failed()
        # The user supplies one share; the server adds its own share unless a second user-held one is given.
        second_share = part2 or (decrypt_cloud_part(user[1]) if user else None) or part1
        try:
            master_key = bytes.fromhex(combine_master_key([part1, second_share]))
        except Exception:
            raise _recovery_failed()
        if not user:
            raise _recovery_failed()
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
        except Exception:
            raise _recovery_failed()
        if not hmac.compare_digest(decrypted_username.encode(), str(user[4]).encode()):
            logger.warning("Recovery verification mismatch: user_id=%s", user[0])
            raise _recovery_failed()
        recovery_token = create_recovery_token(cursor, user[0])
        conn.commit()
        rate_limit.RECOVER_USER.reset(user_key)
        logger.info("Recovery token generated for user ID %s", user[0])
        return {"message": "Password recovery successful.", "recovery_token": recovery_token}
    except HTTPException:
        raise
    except Exception as e:
        logger.error("Unexpected error during recovery: %s", type(e).__name__)
        raise HTTPException(status_code=500, detail="Error during password recovery")
    finally:
        conn.close()

@router.post("/reset-password")
async def reset_password(request: ResetPasswordRequest, http_request: Request):
    ip_key = rate_limit.client_ip(http_request)
    rate_limit.enforce(rate_limit.RESET_IP, ip_key)
    validate_password(request.new_password)
    conn = get_connection()
    cursor = conn.cursor()
    try:
        user_id = tokens.consume_recovery_token(cursor, request.recovery_token)
        if user_id is None:
            conn.rollback()
            rate_limit.RESET_IP.hit(ip_key)
            raise HTTPException(status_code=401, detail="Invalid or expired recovery token")
        password_field = hash_password_with_salt(request.new_password)
        cursor.execute("UPDATE users SET password = ? WHERE id = ?", (password_field, user_id))
        cursor.execute(
            "UPDATE user_sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL",
            (utc_now_iso(), user_id),
        )
        conn.commit()
        logger.warning("Password reset via recovery completed: user_id=%s ip=%s; all sessions revoked", user_id, ip_key)
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        raise HTTPException(status_code=500, detail=f"Error resetting password: {str(e)}")
    finally:
        conn.close()
    from server.websocket import manager
    await manager.close_user_sockets(user_id)
    return {"message": "Password reset successful."}
