from cryptography.fernet import Fernet, InvalidToken

from server.tokens import FERNET

ENCRYPTED_PREFIX = "enc:v1:"


def encrypt_cloud_part(value: str, fernet: Fernet = FERNET) -> str:
    return ENCRYPTED_PREFIX + fernet.encrypt(value.encode()).decode()


def decrypt_cloud_part(stored: str | None, fernet: Fernet = FERNET) -> str | None:
    """Return the share; rows written before encryption was introduced are returned unchanged."""
    if not stored:
        return None
    if not stored.startswith(ENCRYPTED_PREFIX):
        return stored
    try:
        return fernet.decrypt(stored[len(ENCRYPTED_PREFIX):].encode()).decode()
    except InvalidToken:
        return None


def encrypt_legacy_cloud_parts(cursor) -> int:
    cursor.execute(
        "SELECT id, encrypted_cloud_part FROM users "
        "WHERE encrypted_cloud_part IS NOT NULL AND encrypted_cloud_part <> '' "
        "AND encrypted_cloud_part NOT LIKE 'enc:v1:%'"
    )
    rows = cursor.fetchall()
    for row in rows:
        cursor.execute(
            "UPDATE users SET encrypted_cloud_part = ? WHERE id = ?",
            (encrypt_cloud_part(row[1]), row[0]),
        )
    return len(rows)