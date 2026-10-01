from pathlib import Path

from fastapi import APIRouter, HTTPException, Request
from fastapi.responses import FileResponse
from jose import JWTError

from server import tokens
from server.database import get_connection
from server.media_access import (
    can_access_media,
    is_attachment_path,
    is_public_media,
    normalize_media_path,
)
from server.routes.auth import MEDIA_COOKIE_NAME, get_active_session
from server.tokens import TOKEN_ACCESS, TOKEN_MEDIA

router = APIRouter()

STATIC_ROOT = Path("static")
INLINE_SUFFIXES = {
    ".jpg", ".jpeg", ".png", ".gif", ".webp",
    ".mp3", ".wav", ".ogg", ".opus", ".webm", ".mp4", ".mov",
    ".pdf",
}


def _bearer_token(request: Request) -> str | None:
    scheme, _, value = request.headers.get("authorization", "").partition(" ")
    return value.strip() if scheme.lower() == "bearer" and value.strip() else None


def _identify_user(cursor, request: Request) -> int | None:
    """Resolve the caller from a bearer access token or, for <img>/<audio> loads, the media cookie."""
    candidates = []
    bearer = _bearer_token(request)
    if bearer:
        candidates.append((bearer, TOKEN_ACCESS))
    cookie = request.cookies.get(MEDIA_COOKIE_NAME)
    if cookie:
        candidates.append((cookie, TOKEN_MEDIA))
    for token, token_type in candidates:
        try:
            payload = tokens.decode_token(token, token_type)
            user_id = int(payload["sub"])
            session_id = payload.get("sid")
        except (JWTError, KeyError, ValueError):
            continue
        if session_id and get_active_session(cursor, user_id, session_id):
            return user_id
    return None


def _resolve_file(rel_path: str) -> Path:
    root = STATIC_ROOT.resolve()
    candidate = (root / rel_path).resolve()
    if not candidate.is_relative_to(root) or not candidate.is_file():
        raise HTTPException(status_code=404, detail="Not found")
    return candidate


@router.api_route("/static/{file_path:path}", methods=["GET", "HEAD"], include_in_schema=False)
def serve_media(file_path: str, request: Request):
    rel_path = normalize_media_path(file_path)
    if rel_path is None:
        raise HTTPException(status_code=404, detail="Not found")

    if not is_public_media(rel_path):
        conn = get_connection()
        try:
            cursor = conn.cursor()
            user_id = _identify_user(cursor, request)
            if user_id is None:
                raise HTTPException(
                    status_code=401,
                    detail="Authentication required",
                    headers={"WWW-Authenticate": "Bearer"},
                )
            # Same answer for "missing" and "forbidden" so file names cannot be probed.
            if not can_access_media(cursor, user_id, rel_path):
                raise HTTPException(status_code=404, detail="Not found")
        finally:
            conn.close()

    path = _resolve_file(rel_path)
    headers = {"X-Content-Type-Options": "nosniff"}
    if not is_public_media(rel_path):
        headers["Cache-Control"] = "private, no-cache"
        headers["Vary"] = "Authorization, Cookie"

    as_attachment = request.query_params.get("download") == "1" or path.suffix.lower() not in INLINE_SUFFIXES
    download_name = path.name.split("_", 1)[1] if is_attachment_path(rel_path) and "_" in path.name else path.name
    return FileResponse(
        path,
        headers=headers,
        filename=download_name if as_attachment else None,
        content_disposition_type="attachment" if as_attachment else "inline",
    )
