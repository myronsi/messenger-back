"""Username rules shared by registration and lookups."""
import re

from fastapi import HTTPException

USERNAME_PATTERN = re.compile(r"[a-z0-9_]{3,32}")
USERNAME_RULES_MESSAGE = "Username must be 3-32 characters and contain only letters, digits and underscores"


def normalize_username(raw: str | None) -> str:
    """Lowercase a new username and reject anything outside [a-z0-9_]{3,32} (no trimming: spaces are invalid)."""
    value = raw or ""
    if not value.isascii() or not USERNAME_PATTERN.fullmatch(value.lower()):
        raise HTTPException(status_code=400, detail=USERNAME_RULES_MESSAGE)
    return value.lower()


def escape_like(value: str) -> str:
    """Make %, _ and the escape character literal inside a LIKE pattern (use with ESCAPE '\\')."""
    return value.replace("\\", "\\\\").replace("%", "\\%").replace("_", "\\_")
