"""Content-based validation for user uploads (stored XSS hardening).

File extensions are attacker controlled, so anything that may be displayed inline is
checked against its magic bytes, and avatars are decoded and re-encoded.
"""
import io
import re
from pathlib import Path

from fastapi import HTTPException

AVATAR_MAX_BYTES = 5 * 1024 * 1024
AVATAR_MAX_PIXELS = 40_000_000
AVATAR_MAX_SIDE = 1024

MIME_BY_EXTENSION = {
    ".jpg": {"image/jpeg"},
    ".jpeg": {"image/jpeg"},
    ".png": {"image/png"},
    ".gif": {"image/gif"},
    ".webp": {"image/webp"},
    ".mp4": {"video/mp4"},
    ".mov": {"video/quicktime", "video/mp4"},
    ".mp3": {"audio/mpeg"},
    ".wav": {"audio/wav"},
    ".ogg": {"audio/ogg"},
    ".opus": {"audio/ogg", "video/webm"},
    ".webm": {"video/webm", "audio/ogg"},
    ".pdf": {"application/pdf"},
}
INLINE_EXTENSIONS = frozenset(MIME_BY_EXTENSION)

_MP3_FRAME_SYNC = re.compile(rb"^\xff[\xe2-\xff]")


def sniff_mime(head: bytes) -> str | None:
    """Detect a safe media type from the first bytes of a file; None when it is not one of them."""
    if head.startswith(b"\xff\xd8\xff"):
        return "image/jpeg"
    if head.startswith(b"\x89PNG\r\n\x1a\n"):
        return "image/png"
    if head[:6] in (b"GIF87a", b"GIF89a"):
        return "image/gif"
    if head[:4] == b"RIFF" and head[8:12] == b"WEBP":
        return "image/webp"
    if head[:4] == b"RIFF" and head[8:12] == b"WAVE":
        return "audio/wav"
    if head.startswith(b"%PDF-"):
        return "application/pdf"
    if head.startswith(b"OggS"):
        return "audio/ogg"
    if head.startswith(b"\x1a\x45\xdf\xa3"):
        return "video/webm"
    if head[4:8] == b"ftyp":
        return "video/quicktime" if head[8:12] == b"qt  " else "video/mp4"
    if head.startswith(b"ID3") or _MP3_FRAME_SYNC.match(head):
        return "audio/mpeg"
    return None


def content_matches_extension(extension: str, head: bytes) -> bool:
    expected = MIME_BY_EXTENSION.get(extension.lower())
    return expected is None or sniff_mime(head) in expected


def safe_filename(filename: str | None, default: str = "file") -> str:
    name = Path((filename or "").replace("\\", "/")).name.strip()
    return name if name and name not in {".", ".."} else default


def ensure_inline_content_is_genuine(extension: str, content: bytes) -> None:
    """Reject files whose extension promises inline-safe media that the bytes do not contain."""
    if not content_matches_extension(extension, content[:32]):
        raise HTTPException(status_code=400, detail="File content does not match its type")


def process_avatar(content: bytes) -> tuple[bytes, str]:
    """Validate and re-encode an avatar. Returns (bytes, extension); nothing user supplied is kept."""
    if not content:
        raise HTTPException(status_code=400, detail="Empty file")
    if len(content) > AVATAR_MAX_BYTES:
        raise HTTPException(status_code=400, detail="Avatar size exceeds 5 MB limit")
    if sniff_mime(content[:32]) not in {"image/jpeg", "image/png", "image/gif", "image/webp"}:
        raise HTTPException(status_code=400, detail="Avatar must be a JPEG, PNG, GIF or WebP image")

    from PIL import Image, ImageOps, UnidentifiedImageError

    try:
        with Image.open(io.BytesIO(content)) as image:
            width, height = image.size
            if width * height > AVATAR_MAX_PIXELS:
                raise HTTPException(status_code=400, detail="Avatar dimensions are too large")
            image.seek(0)
            image = ImageOps.exif_transpose(image)
            image.thumbnail((AVATAR_MAX_SIDE, AVATAR_MAX_SIDE))
            has_alpha = image.mode in ("RGBA", "LA", "PA") or "transparency" in image.info
            output = io.BytesIO()
            if has_alpha:
                image.convert("RGBA").save(output, format="PNG", optimize=True)
                return output.getvalue(), ".png"
            image.convert("RGB").save(output, format="JPEG", quality=90, optimize=True)
            return output.getvalue(), ".jpg"
    except HTTPException:
        raise
    except (UnidentifiedImageError, OSError, ValueError, Image.DecompressionBombError):
        raise HTTPException(status_code=400, detail="Invalid image file")
