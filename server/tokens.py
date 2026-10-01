import base64
import hashlib
import os
import secrets
from datetime import datetime, timedelta, timezone

from cryptography.fernet import Fernet
from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.kdf.hkdf import HKDF
from jose import JWTError, jwt

ALGORITHM = "HS256"
MIN_SECRET_KEY_LENGTH = 32
# The value that used to be hardcoded in source; only used to migrate data encrypted with it.
LEGACY_PLACEHOLDER_KEY = "supersecretkey"

TOKEN_ACCESS = "access"
TOKEN_RECOVERY = "recovery"
TOKEN_TWO_FACTOR = "2fa_login"
_AUDIENCE_PREFIX = "messenger:"

TWO_FACTOR_MAX_ATTEMPTS = 5


def load_secret_key(environ=None) -> str:
    value = (environ if environ is not None else os.environ).get("SECRET_KEY", "").strip()
    if len(value) < MIN_SECRET_KEY_LENGTH:
        raise RuntimeError(
            f"SECRET_KEY must be set to a random value of at least {MIN_SECRET_KEY_LENGTH} characters. "
            'Generate one with: python -c "import secrets; print(secrets.token_urlsafe(48))"'
        )
    return value


def derive_fernet_key(secret_key: str) -> bytes:
    """Derive a key for encrypting TOTP secrets that is independent from the JWT signing key."""
    derived = HKDF(
        algorithm=hashes.SHA256(),
        length=32,
        salt=None,
        info=b"messenger-totp-encryption-v1",
    ).derive(secret_key.encode())
    return base64.urlsafe_b64encode(derived)


def legacy_fernet_key() -> bytes:
    return base64.urlsafe_b64encode(hashlib.sha256(LEGACY_PLACEHOLDER_KEY.encode()).digest())


SECRET_KEY = load_secret_key()
FERNET = Fernet(derive_fernet_key(SECRET_KEY))


def utc_now() -> datetime:
    return datetime.now(timezone.utc)


def audience_for(token_type: str) -> str:
    return _AUDIENCE_PREFIX + token_type


def create_token(token_type: str, user_id: int, expires_delta: timedelta, **claims) -> str:
    now = utc_now()
    payload = {
        **claims,
        "sub": str(user_id),
        "type": token_type,
        "aud": audience_for(token_type),
        "iat": now,
        "exp": now + expires_delta,
    }
    return jwt.encode(payload, SECRET_KEY, algorithm=ALGORITHM)


def decode_token(token: str, token_type: str) -> dict:
    """Verify signature, expiry, audience and type; raise JWTError if any of them is wrong."""
    payload = jwt.decode(
        token,
        SECRET_KEY,
        algorithms=[ALGORITHM],
        options={"verify_aud": False, "require_exp": True, "require_sub": True},
    )
    if payload.get("type") != token_type or payload.get("aud") != audience_for(token_type):
        raise JWTError("Unexpected token type")
    return payload


def new_jti() -> str:
    return secrets.token_urlsafe(24)


def hash_jti(jti: str) -> str:
    return hashlib.sha256(jti.encode()).hexdigest()


def purge_expired_token_records(cursor) -> None:
    cutoff = utc_now() - timedelta(days=1)
    cursor.execute("DELETE FROM recovery_tokens WHERE expires_at < ?", (cutoff,))
    cursor.execute("DELETE FROM two_factor_challenges WHERE expires_at < ?", (cutoff,))


def issue_recovery_token(cursor, user_id: int, expires_delta: timedelta) -> str:
    purge_expired_token_records(cursor)
    jti = new_jti()
    cursor.execute(
        "INSERT INTO recovery_tokens (jti_hash, user_id, expires_at) VALUES (?, ?, ?)",
        (hash_jti(jti), user_id, utc_now() + expires_delta),
    )
    return create_token(TOKEN_RECOVERY, user_id, expires_delta, jti=jti)


def consume_recovery_token(cursor, token: str) -> int | None:
    """Return the user id and mark the token used, or None if it is invalid, expired or already used."""
    try:
        payload = decode_token(token, TOKEN_RECOVERY)
        user_id = int(payload["sub"])
        jti = payload["jti"]
    except (JWTError, KeyError, TypeError, ValueError):
        return None
    now = utc_now()
    cursor.execute(
        """
        UPDATE recovery_tokens SET used_at = ?
        WHERE jti_hash = ? AND user_id = ? AND used_at IS NULL AND expires_at > ?
        RETURNING user_id
        """,
        (now, hash_jti(jti), user_id, now),
    )
    return user_id if cursor.fetchone() else None


def issue_two_factor_challenge(cursor, user_id: int, expires_delta: timedelta) -> str:
    purge_expired_token_records(cursor)
    jti = new_jti()
    cursor.execute(
        "INSERT INTO two_factor_challenges (jti_hash, user_id, expires_at) VALUES (?, ?, ?)",
        (hash_jti(jti), user_id, utc_now() + expires_delta),
    )
    return create_token(TOKEN_TWO_FACTOR, user_id, expires_delta, jti=jti)


def begin_two_factor_attempt(cursor, challenge: str) -> tuple[int, str] | None:
    """Count an attempt on a live challenge; return (user_id, jti_hash) or None if it cannot be used."""
    try:
        payload = decode_token(challenge, TOKEN_TWO_FACTOR)
        user_id = int(payload["sub"])
        jti = payload["jti"]
    except (JWTError, KeyError, TypeError, ValueError):
        return None
    jti_hash = hash_jti(jti)
    cursor.execute(
        """
        UPDATE two_factor_challenges SET attempts = attempts + 1
        WHERE jti_hash = ? AND user_id = ? AND used_at IS NULL AND expires_at > ? AND attempts < ?
        RETURNING user_id
        """,
        (jti_hash, user_id, utc_now(), TWO_FACTOR_MAX_ATTEMPTS),
    )
    return (user_id, jti_hash) if cursor.fetchone() else None


def finish_two_factor_challenge(cursor, jti_hash: str) -> None:
    cursor.execute("UPDATE two_factor_challenges SET used_at = ? WHERE jti_hash = ?", (utc_now(), jti_hash))
