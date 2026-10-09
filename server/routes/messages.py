import json
from fastapi import APIRouter, HTTPException, Depends, status, UploadFile, File, Form, Query
from pydantic import BaseModel
from starlette.concurrency import run_in_threadpool
from server.database import get_connection
from server.routes.auth import get_current_user
from server.websocket import manager, send_chat_list_message
from server.time_utils import to_utc_iso, utc_now_iso
from server.chat_summary import (
    deleted_for_user_ids,
    get_chat_unread_summary,
    get_message_summary,
    message_visible_to,
    not_deleted_for_sql,
)
from server.image_metadata import IMAGE_METADATA_KEYS, describe_image, thumbnail_extension
from server.media_access import copy_attachments, record_attachment
from server.upload_security import ensure_inline_content_is_genuine, safe_filename
from server.privacy import DEFAULT_AVATAR, can_send_to_chat, read_receipts_enabled, serialize_user_snapshot
from pathlib import Path
import uuid
import logging
import subprocess
import sys
from array import array

router = APIRouter()

logger = logging.getLogger(__name__)

WAVEFORM_BAR_COUNT = 38
WAVEFORM_SAMPLE_RATE = 8000

class Message(BaseModel):
    chat_id: int
    content: str

class MessageEdit(BaseModel):
    content: str    

class ForwardMessageRequest(BaseModel):
    source_message_id: int
    target_chat_ids: list[int]

def _parse_json_list(value):
    if not value:
        return []
    if isinstance(value, list):
        return value
    try:
        parsed = json.loads(value)
    except (json.JSONDecodeError, TypeError):
        return []
    return parsed if isinstance(parsed, list) else []

def _hydrate_user_items(cursor, items, requester_id: int, respect_read_receipts: bool = False):
    user_ids = sorted({
        item.get("user_id") or item.get("id")
        for item in items
        if isinstance(item, dict) and (item.get("user_id") or item.get("id"))
    })
    if not user_ids:
        return [item for item in items if isinstance(item, dict)]

    placeholders = ",".join(["?"] * len(user_ids))
    cursor.execute(
        f"SELECT id, username, display_name, avatar_url FROM users WHERE id IN ({placeholders})",
        user_ids,
    )
    users = {row["id"]: row for row in cursor.fetchall()}

    hydrated = []
    for item in items:
        if not isinstance(item, dict):
            continue
        item_user_id = item.get("user_id") or item.get("id")
        user = users.get(item_user_id)
        if respect_read_receipts and user and not read_receipts_enabled(cursor, user["id"]):
            continue
        snapshot = serialize_user_snapshot(cursor, item_user_id, requester_id)
        hydrated.append({
            **item,
            "user_id": item_user_id,
            "username": item.get("username") or snapshot["username"],
            "display_name": item.get("display_name") or snapshot["display_name"],
            "avatar_url": snapshot["avatar_url"],
        })
    return hydrated


FILE_MESSAGE_LIKE_PATTERN = '%"file_url"%'


def _message_visible_to(message, user_id: int) -> bool:
    return message_visible_to(message, user_id)


def _is_image_file(content: dict) -> bool:
    file_name = str(content.get("file_name") or "")
    file_type = str(content.get("file_type") or "")
    return file_type == "image" or Path(file_name).suffix.lower() in {".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".avif"}


def _audio_kind(content: dict) -> str | None:
    file_name = str(content.get("file_name") or "")
    file_type = str(content.get("file_type") or "")
    extension = Path(file_name).suffix.lower()
    if file_type == "voice" or extension == ".opus":
        return "voice"
    if file_type == "audio" or extension in {".mp3", ".wav", ".ogg", ".m4a", ".aac", ".flac"}:
        return "file"
    return None


def _parse_message_content(content: str):
    if content and content.startswith("{"):
        try:
            parsed_content = json.loads(content)
            if isinstance(parsed_content, dict):
                return parsed_content, "file" if parsed_content.get("file_url") else "message"
        except json.JSONDecodeError:
            pass
    return content, "message"


def _audio_metadata_from_row(row) -> dict | None:
    if "audio_duration" not in row.keys() and "audio_waveform" not in row.keys():
        return None

    metadata: dict = {}
    duration = row["audio_duration"] if "audio_duration" in row.keys() else None
    waveform_value = row["audio_waveform"] if "audio_waveform" in row.keys() else None

    if duration is not None:
        metadata["duration"] = duration

    if waveform_value:
        try:
            waveform = json.loads(waveform_value)
            if isinstance(waveform, list):
                metadata["waveform"] = waveform
        except (json.JSONDecodeError, TypeError):
            pass

    return metadata or None


def _with_audio_metadata(content: dict, row) -> dict:
    metadata = _audio_metadata_from_row(row)
    if not metadata:
        return content
    return {
        **content,
        "audio_metadata": metadata,
    }


def _forwarded_from_from_row(row) -> dict | None:
    if "forwarded_from_message_id" not in row.keys():
        return None
    if row["forwarded_from_message_id"] is None:
        return None
    return {
        "message_id": row["forwarded_from_message_id"],
        "sender_id": row["forwarded_from_sender_id"],
        "sender_name": row["forwarded_from_sender_name"],
        "sender_username": row["forwarded_from_sender_username"],
    }


def _message_kind_and_payload(content: str, row=None) -> tuple[str, str | dict]:
    parsed_content, message_type = _parse_message_content(content)
    if message_type == "file" and isinstance(parsed_content, dict) and row is not None:
        parsed_content = _with_audio_metadata(parsed_content, row)
    return message_type, parsed_content


def _extract_voice_audio_metadata(file_path: Path) -> dict:
    metadata: dict = {}
    duration = 0.0

    try:
        probe = subprocess.run(
            [
                "ffprobe",
                "-v",
                "error",
                "-show_entries",
                "format=duration",
                "-of",
                "default=noprint_wrappers=1:nokey=1",
                str(file_path),
            ],
            capture_output=True,
            text=True,
            timeout=10,
            check=False,
        )
        if probe.returncode == 0:
            duration = float((probe.stdout or "").strip() or 0)
    except (OSError, ValueError, subprocess.TimeoutExpired) as exc:
        logger.warning(f"Could not read audio duration: {type(exc).__name__}")

    try:
        decoded = subprocess.run(
            [
                "ffmpeg",
                "-v",
                "error",
                "-i",
                str(file_path),
                "-ac",
                "1",
                "-ar",
                str(WAVEFORM_SAMPLE_RATE),
                "-f",
                "s16le",
                "pipe:1",
            ],
            capture_output=True,
            timeout=20,
            check=False,
        )
        if decoded.returncode != 0 or not decoded.stdout:
            raise RuntimeError((decoded.stderr or b"").decode("utf-8", errors="ignore") or "ffmpeg decode failed")

        samples = array("h")
        samples.frombytes(decoded.stdout)
        if sys.byteorder != "little":
            samples.byteswap()

        if not samples:
            return metadata

        if duration <= 0:
            duration = len(samples) / WAVEFORM_SAMPLE_RATE

        samples_per_bar = max(1, len(samples) // WAVEFORM_BAR_COUNT)
        raw_bars = []
        for bar_index in range(WAVEFORM_BAR_COUNT):
            start = bar_index * samples_per_bar
            end = len(samples) if bar_index == WAVEFORM_BAR_COUNT - 1 else min(len(samples), start + samples_per_bar)
            step = max(1, (end - start) // 120)
            total = 0.0
            count = 0

            for sample_index in range(start, end, step):
                sample = samples[sample_index] / 32768
                total += sample * sample
                count += 1

            raw_bars.append((total / count) ** 0.5 if count else 0.0)

        peak = max(max(raw_bars), 0.001)
        metadata["waveform"] = [
            round(min(1.0, max(0.16, value / peak)), 4)
            for value in raw_bars
        ]
    except (OSError, RuntimeError, subprocess.TimeoutExpired) as exc:
        logger.warning(f"Could not extract waveform: {type(exc).__name__}")

    if duration > 0:
        metadata["duration"] = round(duration, 3)

    return metadata


def _undelivered_recipients_for_send(cursor, chat_id: int, sender_id: int) -> tuple[list[int], str | None]:
    if can_send_to_chat(cursor, chat_id, sender_id):
        return [], None

    cursor.execute("SELECT type, user1_id, user2_id FROM chats WHERE id = ?", (chat_id,))
    chat = cursor.fetchone()
    if not chat or chat["type"] != "one-on-one":
        return [], "This message could not be delivered."

    other_id = chat["user2_id"] if chat["user1_id"] == sender_id else chat["user1_id"]
    if not other_id:
        return [], "This message could not be delivered."
    return [other_id], "This message could not be delivered due to the recipient's privacy settings."

@router.post("/upload")
async def upload_file(
    chat_id: int = Form(...),
    caption: str | None = Form(None),
    file: UploadFile = File(...),
    current_user: dict = Depends(get_current_user)
):
    MAX_FILE_SIZE = 10 * 1024 * 1024
    ALLOWED_FILE_TYPES = {
        "image": [".jpg", ".jpeg", ".png", ".gif"],
        "video": [".mp4", ".mov", ".ogg"],
        "document": [".pdf", ".doc", ".docx", ".txt"],
        "presention": [".pptx"],
        "arcive": [".zip"],
        "audio": [".mp3", ".wav", ".ogg"],
        "code": [".js", ".ts", ".py", ".java", ".cpp", ".html", ".css"],
        "none": [""]
    }

    file_size = 0
    content = await file.read()
    file_size = len(content)
    if file_size > MAX_FILE_SIZE:
        raise HTTPException(status_code=400, detail="File size exceeds 10 MB limit")

    file.filename = safe_filename(file.filename)
    file_extension = Path(file.filename).suffix.lower()
    ensure_inline_content_is_genuine(file_extension, content)
    file_type = None
    for type_, extensions in ALLOWED_FILE_TYPES.items():
        if file_extension in extensions:
            file_type = type_
            break
    if not file_type:
        raise HTTPException(status_code=400, detail="Unsupported file type")

    # Decode before the membership and privacy checks, so nothing can change between those checks and the insert.
    # Decoding a large image takes long enough to stall every WebSocket on the event loop.
    image_fields, thumbnail = {}, None
    if file_type == "image":
        image_fields, thumbnail = await run_in_threadpool(describe_image, content, file_extension)

    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("SELECT * FROM participants WHERE chat_id = ? AND user_id = ?", (chat_id, current_user["id"]))
        if not cursor.fetchone():
            raise HTTPException(status_code=403, detail="You are not a member of this chat")
        undelivered_to, delivery_error = _undelivered_recipients_for_send(cursor, chat_id, current_user["id"])

        upload_dir = Path("static/uploads")
        upload_dir.mkdir(parents=True, exist_ok=True)
        unique_filename = f"{uuid.uuid4()}_{file.filename}"
        file_path = upload_dir / unique_filename
        with file_path.open("wb") as buffer:
            buffer.write(content)

        file_url = f"/static/uploads/{unique_filename}"
        file_name = file.filename
        clean_caption = caption.strip() if caption else ""
        if thumbnail:
            thumbnail_name = f"{uuid.uuid4()}_thumb{thumbnail_extension(thumbnail)}"
            (upload_dir / thumbnail_name).write_bytes(thumbnail)
            image_fields["thumbnail_url"] = f"/static/uploads/{thumbnail_name}"
        message_content = {
            "file_url": file_url,
            "file_name": file_name,
            "file_type": file_type,
            "file_size": file_size,
            **image_fields,
        }
        if clean_caption:
            message_content["caption"] = clean_caption

        cursor.execute("""
            INSERT INTO messages (chat_id, sender_id, sender_name, content, timestamp, delivery_error, undelivered_to)
            VALUES (?, ?, ?, ?, ?, ?, ?)
        """, (chat_id, current_user["id"], current_user["display_name"], json.dumps(message_content), utc_now_iso(), delivery_error, json.dumps(undelivered_to)))
        message_id = cursor.lastrowid
        record_attachment(cursor, message_id, file_url)
        record_attachment(cursor, message_id, image_fields.get("thumbnail_url"))
        conn.commit()

        timestamp = utc_now_iso()

        await manager.broadcast_personalized(chat_id, lambda recipient_id: None if recipient_id in undelivered_to else {
            "type": "file",
            "username": serialize_user_snapshot(cursor, current_user["id"], recipient_id)["display_name"],
            "sender_username": current_user["username"],
            "sender_id": current_user["id"],
            "avatar_url": serialize_user_snapshot(cursor, current_user["id"], recipient_id)["avatar_url"],
            "is_deleted": False,
            "delivery_error": delivery_error if recipient_id == current_user["id"] else None,
            "reactions": [],
            "read_by": [],
            "data": {
                "chat_id": chat_id,
                "file_url": file_url,
                "file_name": file_name,
                "file_type": file_type,
                "file_size": file_size,
                **image_fields,
                **({"caption": clean_caption} if clean_caption else {}),
                "message_id": message_id,
                "reply_to": None
            },
            "timestamp": timestamp,
        })
        await send_chat_list_message(cursor, chat_id, current_user["id"], message_id)
        logger.info(f"File uploaded and broadcasted to chat {chat_id}")

        return {"message": "File uploaded successfully", "file_url": file_url}
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error uploading file: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error uploading file: {str(e)}")
    finally:
        conn.close()

@router.post("/vm")
async def upload_voice_message(
    chat_id: int = Form(...),
    file: UploadFile = File(...),
    current_user: dict = Depends(get_current_user)
):
    MAX_FILE_SIZE = 10 * 1024 * 1024  # 10 MB
    ALLOWED_FILE_TYPES = [".opus", ".webm"]

    file.filename = safe_filename(file.filename, "voice.webm")
    file_extension = Path(file.filename).suffix.lower()
    if file_extension not in ALLOWED_FILE_TYPES:
        raise HTTPException(status_code=400, detail="Only Opus voice recordings are allowed")

    content = await file.read()
    file_size = len(content)
    if file_size > MAX_FILE_SIZE:
        raise HTTPException(status_code=400, detail="File size exceeds 10 MB limit")
    ensure_inline_content_is_genuine(file_extension, content)

    conn = get_connection()
    cursor = conn.cursor()
    try:
        cursor.execute("SELECT * FROM participants WHERE chat_id = ? AND user_id = ?", (chat_id, current_user["id"]))
        if not cursor.fetchone():
            raise HTTPException(status_code=403, detail="You are not a member of this chat")
        undelivered_to, delivery_error = _undelivered_recipients_for_send(cursor, chat_id, current_user["id"])

        upload_dir = Path("static/vm")
        upload_dir.mkdir(parents=True, exist_ok=True)
        unique_filename = f"{uuid.uuid4()}_{file.filename}"
        file_path = upload_dir / unique_filename
        with file_path.open("wb") as buffer:
            buffer.write(content)

        file_url = f"/static/vm/{unique_filename}"
        file_name = file.filename
        file_type = "voice"
        audio_metadata = _extract_voice_audio_metadata(file_path)
        message_content = {
            "file_url": file_url,
            "file_name": file_name,
            "file_type": file_type,
            "file_size": file_size
        }
        audio_duration = audio_metadata.get("duration")
        audio_waveform = json.dumps(audio_metadata["waveform"]) if audio_metadata.get("waveform") else None

        cursor.execute("""
            INSERT INTO messages (
                chat_id, sender_id, sender_name, content, timestamp,
                delivery_error, undelivered_to, audio_duration, audio_waveform
            )
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
        """, (
            chat_id,
            current_user["id"],
            current_user["display_name"],
            json.dumps(message_content),
            utc_now_iso(),
            delivery_error,
            json.dumps(undelivered_to),
            audio_duration,
            audio_waveform,
        ))
        message_id = cursor.lastrowid
        record_attachment(cursor, message_id, file_url)
        conn.commit()

        timestamp = utc_now_iso()

        await manager.broadcast_personalized(chat_id, lambda recipient_id: None if recipient_id in undelivered_to else {
            "type": "file",
            "username": serialize_user_snapshot(cursor, current_user["id"], recipient_id)["display_name"],
            "sender_username": current_user["username"],
            "sender_id": current_user["id"],
            "avatar_url": serialize_user_snapshot(cursor, current_user["id"], recipient_id)["avatar_url"],
            "is_deleted": False,
            "delivery_error": delivery_error if recipient_id == current_user["id"] else None,
            "reactions": [],
            "read_by": [],
            "data": {
                "chat_id": chat_id,
                "file_url": file_url,
                "file_name": file_name,
                "file_type": file_type,
                "file_size": file_size,
                "audio_metadata": audio_metadata,
                "message_id": message_id,
                "reply_to": None
            },
            "timestamp": timestamp,
        })
        await send_chat_list_message(cursor, chat_id, current_user["id"], message_id)
        logger.info(f"Voice message uploaded and broadcasted to chat {chat_id}")

        return {"message": "Voice message uploaded successfully", "file_url": file_url}
    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error uploading voice message: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error uploading voice message: {str(e)}")
    finally:
        conn.close()

@router.post("/forward")
async def forward_message(
    payload: ForwardMessageRequest,
    current_user: dict = Depends(get_current_user)
):
    target_chat_ids = []
    seen_targets = set()
    for chat_id in payload.target_chat_ids:
        if chat_id in seen_targets:
            continue
        seen_targets.add(chat_id)
        target_chat_ids.append(chat_id)

    if not target_chat_ids:
        raise HTTPException(status_code=400, detail="Select at least one chat")

    conn = get_connection()
    cursor = conn.cursor()

    try:
        cursor.execute("""
            SELECT messages.id, messages.chat_id, messages.sender_id, messages.sender_name,
                   messages.content, messages.undelivered_to, messages.deleted_for,
                   messages.audio_duration, messages.audio_waveform,
                   messages.forwarded_from_message_id,
                   messages.forwarded_from_sender_id,
                   messages.forwarded_from_sender_name,
                   messages.forwarded_from_sender_username,
                   users.username AS sender_username
            FROM messages
            LEFT JOIN users ON users.id = messages.sender_id
            WHERE messages.id = ?
        """, (payload.source_message_id,))
        source = cursor.fetchone()
        if not source or not source["content"]:
            raise HTTPException(status_code=404, detail="Message not found")

        cursor.execute(
            "SELECT 1 FROM participants WHERE chat_id = ? AND user_id = ?",
            (source["chat_id"], current_user["id"]),
        )
        if not cursor.fetchone() or not _message_visible_to(source, current_user["id"]):
            raise HTTPException(status_code=403, detail="You cannot forward this message")

        source_forwarded = _forwarded_from_from_row(source)
        forwarded_from = source_forwarded or {
            "message_id": source["id"],
            "sender_id": source["sender_id"],
            "sender_name": source["sender_name"],
            "sender_username": source["sender_username"],
        }

        forwarded = []
        failed = []

        for target_chat_id in target_chat_ids:
            cursor.execute("SELECT id FROM chats WHERE id = ?", (target_chat_id,))
            if not cursor.fetchone():
                failed.append({"chat_id": target_chat_id, "reason": "Chat not found"})
                continue

            cursor.execute(
                "SELECT 1 FROM participants WHERE chat_id = ? AND user_id = ?",
                (target_chat_id, current_user["id"]),
            )
            if not cursor.fetchone():
                failed.append({"chat_id": target_chat_id, "reason": "You are not a member of this chat"})
                continue

            undelivered_to, delivery_error = _undelivered_recipients_for_send(cursor, target_chat_id, current_user["id"])
            timestamp = utc_now_iso()

            try:
                cursor.execute("""
                    INSERT INTO messages (
                        chat_id, sender_id, sender_name, content, timestamp,
                        delivery_error, undelivered_to, audio_duration, audio_waveform,
                        forwarded_from_message_id, forwarded_from_sender_id,
                        forwarded_from_sender_name, forwarded_from_sender_username
                    )
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """, (
                    target_chat_id,
                    current_user["id"],
                    current_user["display_name"],
                    source["content"],
                    timestamp,
                    delivery_error,
                    json.dumps(undelivered_to),
                    source["audio_duration"],
                    source["audio_waveform"],
                    forwarded_from["message_id"],
                    forwarded_from["sender_id"],
                    forwarded_from["sender_name"],
                    forwarded_from["sender_username"],
                ))
                new_message_id = cursor.lastrowid
                copy_attachments(cursor, source["id"], new_message_id)
                conn.commit()
            except Exception as exc:
                conn.rollback()
                logger.error(f"Error forwarding message {source['id']} to chat {target_chat_id}: {exc}")
                failed.append({"chat_id": target_chat_id, "reason": "Failed to forward message"})
                continue

            message_kind, content_payload = _message_kind_and_payload(source["content"], source)
            forwarded.append({"chat_id": target_chat_id, "message_id": new_message_id})

            def build_forwarded_message(recipient_id):
                if recipient_id in undelivered_to:
                    return None
                sender_snapshot = serialize_user_snapshot(cursor, current_user["id"], recipient_id)
                return {
                    "type": message_kind,
                    "username": sender_snapshot["display_name"],
                    "sender_username": current_user["username"],
                    "sender_id": current_user["id"],
                    "avatar_url": sender_snapshot["avatar_url"],
                    "is_deleted": False,
                    "delivery_error": delivery_error if recipient_id == current_user["id"] else None,
                    "reactions": [],
                    "read_by": [],
                    "forwarded_from": forwarded_from,
                    "data": {
                        "chat_id": target_chat_id,
                        **(content_payload if message_kind == "file" else {"content": content_payload}),
                        "message_id": new_message_id,
                        "reply_to": None,
                    },
                    "timestamp": timestamp,
                }

            await manager.broadcast_personalized(target_chat_id, build_forwarded_message)
            await send_chat_list_message(cursor, target_chat_id, current_user["id"], new_message_id)

        return {"forwarded": forwarded, "failed": failed}
    except HTTPException:
        raise
    except Exception as exc:
        conn.rollback()
        logger.error(f"Error forwarding message {payload.source_message_id}: {exc}")
        raise HTTPException(status_code=500, detail="Failed to forward message")
    finally:
        conn.close()

@router.get("/photos/{chat_id}")
async def get_chat_photos(
    chat_id: int,
    current_user: dict = Depends(get_current_user)
):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        cursor.execute("SELECT 1 FROM participants WHERE chat_id = ? AND user_id = ?", (chat_id, current_user["id"]))
        if not cursor.fetchone():
            raise HTTPException(status_code=403, detail="You are not a member of this chat")

        cursor.execute(f"""
            SELECT id, sender_id, content, timestamp, undelivered_to
            FROM messages
            WHERE chat_id = ?
              AND content LIKE ?
              AND {not_deleted_for_sql("deleted_for")}
            ORDER BY id DESC
        """, (chat_id, FILE_MESSAGE_LIKE_PATTERN, current_user["id"]))

        photos = []
        for msg in cursor.fetchall():
            if not _message_visible_to(msg, current_user["id"]):
                continue

            try:
                content = json.loads(msg["content"])
            except (json.JSONDecodeError, TypeError):
                continue

            if not isinstance(content, dict) or not content.get("file_url") or not _is_image_file(content):
                continue

            file_name = content.get("file_name") or "Photo"
            file_url = content.get("file_url")
            photos.append({
                "id": msg["id"],
                "file_url": file_url,
                "url": file_url,
                "file_name": file_name,
                "name": file_name,
                "file_type": content.get("file_type") or "image",
                "file_size": content.get("file_size"),
                **{key: content[key] for key in IMAGE_METADATA_KEYS if key in content},
                "timestamp": to_utc_iso(msg["timestamp"]),
            })

        return {"photos": photos}
    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"Error loading photos for chat {chat_id}: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error loading photos: {str(e)}")
    finally:
        conn.close()

@router.get("/audios/{chat_id}")
async def get_chat_audios(
    chat_id: int,
    current_user: dict = Depends(get_current_user)
):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        cursor.execute("SELECT 1 FROM participants WHERE chat_id = ? AND user_id = ?", (chat_id, current_user["id"]))
        if not cursor.fetchone():
            raise HTTPException(status_code=403, detail="You are not a member of this chat")

        cursor.execute(f"""
            SELECT id, sender_id, content, timestamp, undelivered_to, audio_duration, audio_waveform
            FROM messages
            WHERE chat_id = ?
              AND content LIKE ?
              AND {not_deleted_for_sql("deleted_for")}
            ORDER BY id DESC
        """, (chat_id, FILE_MESSAGE_LIKE_PATTERN, current_user["id"]))

        audios = []
        for msg in cursor.fetchall():
            if not _message_visible_to(msg, current_user["id"]):
                continue

            parsed_content, message_type = _parse_message_content(msg["content"])
            if message_type != "file" or not isinstance(parsed_content, dict) or not parsed_content.get("file_url"):
                continue

            parsed_content = _with_audio_metadata(parsed_content, msg)
            audio_kind = _audio_kind(parsed_content)
            if not audio_kind:
                continue

            file_name = parsed_content.get("file_name") or ("Voice message" if audio_kind == "voice" else "Audio")
            file_url = parsed_content.get("file_url")
            audios.append({
                "id": msg["id"],
                "file_url": file_url,
                "url": file_url,
                "file_name": file_name,
                "name": file_name,
                "file_type": parsed_content.get("file_type") or ("voice" if audio_kind == "voice" else "audio"),
                "file_size": parsed_content.get("file_size"),
                "audio_metadata": parsed_content.get("audio_metadata"),
                "audio_kind": audio_kind,
                "timestamp": to_utc_iso(msg["timestamp"]),
            })

        return {"audios": audios}
    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"Error loading audios for chat {chat_id}: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error loading audios: {str(e)}")
    finally:
        conn.close()

@router.get("/search/{chat_id}")
async def search_chat_messages(
    chat_id: int,
    q: str = Query(..., min_length=1),
    current_user: dict = Depends(get_current_user)
):
    query = q.strip().lower()
    if not query:
        return {"results": []}

    conn = get_connection()
    cursor = conn.cursor()

    try:
        cursor.execute("SELECT 1 FROM participants WHERE chat_id = ? AND user_id = ?", (chat_id, current_user["id"]))
        if not cursor.fetchone():
            raise HTTPException(status_code=403, detail="You are not a member of this chat")

        cursor.execute(f"""
            SELECT messages.id, messages.sender_id, messages.content, messages.timestamp,
                   messages.audio_duration, messages.audio_waveform,
                   messages.forwarded_from_message_id,
                   messages.forwarded_from_sender_id,
                   messages.forwarded_from_sender_name,
                   messages.forwarded_from_sender_username,
                   COALESCE(users.display_name, messages.sender_name) AS sender,
                   users.username AS sender_username,
                   users.avatar_url,
                   messages.undelivered_to
            FROM messages
            LEFT JOIN users ON messages.sender_id = users.id
            WHERE messages.chat_id = ?
              AND {not_deleted_for_sql()}
            ORDER BY messages.id DESC
        """, (chat_id, current_user["id"]))

        results = []
        for msg in cursor.fetchall():
            if not _message_visible_to(msg, current_user["id"]):
                continue

            parsed_content, message_type = _parse_message_content(msg["content"])
            if message_type == "file" and isinstance(parsed_content, dict):
                parsed_content = _with_audio_metadata(parsed_content, msg)
            if isinstance(parsed_content, dict):
                searchable_text = " ".join(
                    str(parsed_content.get(key) or "")
                    for key in ("file_name", "file_type", "caption")
                )
            else:
                searchable_text = str(parsed_content or "")

            if query not in searchable_text.lower():
                continue

            sender_snapshot = serialize_user_snapshot(cursor, msg["sender_id"], current_user["id"])
            results.append({
                "id": msg["id"],
                "sender_id": msg["sender_id"],
                "sender": sender_snapshot["display_name"] or msg["sender"],
                "sender_username": msg["sender_username"],
                "avatar_url": sender_snapshot["avatar_url"],
                "content": parsed_content,
                "type": message_type,
                "forwarded_from": _forwarded_from_from_row(msg),
                "timestamp": to_utc_iso(msg["timestamp"]),
            })

        return {"results": results}
    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"Error searching messages for chat {chat_id}: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error searching messages: {str(e)}")
    finally:
        conn.close()

@router.get("/history/{chat_id}")
async def get_message_history(
    chat_id: int,
    limit: int = Query(50, ge=1, le=100),
    before_id: int | None = Query(None, ge=1),
    after_id: int | None = Query(None, ge=1),
    around_id: int | None = Query(None, ge=1),
    current_user: dict = Depends(get_current_user)
):
    conn = get_connection()
    cursor = conn.cursor()

    try:
        cursor.execute("SELECT * FROM participants WHERE chat_id = ? AND user_id = ?", (chat_id, current_user["id"]))
        if not cursor.fetchone():
            logger.error(f"User {current_user['id']} is not a member of chat {chat_id}")
            raise HTTPException(status_code=403, detail="You are not a member of this chat")

        history_limit = limit + 1
        not_deleted_sql = not_deleted_for_sql()
        has_more_before = False
        has_more_after = False
        if around_id:
            before_limit = max(0, limit // 2)
            after_limit = max(0, limit - before_limit - 1)
            cursor.execute(f"""
                SELECT messages.id, messages.sender_id, messages.content, messages.timestamp,
                       messages.audio_duration, messages.audio_waveform,
                       messages.forwarded_from_message_id,
                       messages.forwarded_from_sender_id,
                       messages.forwarded_from_sender_name,
                       messages.forwarded_from_sender_username,
                       COALESCE(users.display_name, messages.sender_name) AS sender,
                       users.username AS sender_username,
                       messages.reply_to, messages.reactions, users.avatar_url, messages.read_by,
                       messages.edited_at, messages.delivery_error, messages.undelivered_to
                FROM messages
                LEFT JOIN users ON messages.sender_id = users.id
                WHERE messages.chat_id = ? AND messages.id <= ?
                  AND {not_deleted_sql}
                ORDER BY messages.id DESC
                LIMIT ?
            """, (chat_id, around_id, current_user["id"], before_limit + 2))
            before_rows = cursor.fetchall()
            has_more_before = len(before_rows) > before_limit + 1
            before_rows = list(reversed(before_rows[:before_limit + 1]))

            cursor.execute(f"""
                SELECT messages.id, messages.sender_id, messages.content, messages.timestamp,
                       messages.audio_duration, messages.audio_waveform,
                       messages.forwarded_from_message_id,
                       messages.forwarded_from_sender_id,
                       messages.forwarded_from_sender_name,
                       messages.forwarded_from_sender_username,
                       COALESCE(users.display_name, messages.sender_name) AS sender,
                       users.username AS sender_username,
                       messages.reply_to, messages.reactions, users.avatar_url, messages.read_by,
                       messages.edited_at, messages.delivery_error, messages.undelivered_to
                FROM messages
                LEFT JOIN users ON messages.sender_id = users.id
                WHERE messages.chat_id = ? AND messages.id > ?
                  AND {not_deleted_sql}
                ORDER BY messages.id ASC
                LIMIT ?
            """, (chat_id, around_id, current_user["id"], after_limit + 1))
            after_rows = cursor.fetchall()
            has_more_after = len(after_rows) > after_limit
            messages = before_rows + after_rows[:after_limit]
            has_more = has_more_before
        elif after_id:
            cursor.execute(f"""
                SELECT messages.id, messages.sender_id, messages.content, messages.timestamp,
                       messages.audio_duration, messages.audio_waveform,
                       messages.forwarded_from_message_id,
                       messages.forwarded_from_sender_id,
                       messages.forwarded_from_sender_name,
                       messages.forwarded_from_sender_username,
                       COALESCE(users.display_name, messages.sender_name) AS sender,
                       users.username AS sender_username,
                       messages.reply_to, messages.reactions, users.avatar_url, messages.read_by,
                       messages.edited_at, messages.delivery_error, messages.undelivered_to
                FROM messages
                LEFT JOIN users ON messages.sender_id = users.id
                WHERE messages.chat_id = ? AND messages.id > ?
                  AND {not_deleted_sql}
                ORDER BY messages.id ASC
                LIMIT ?
            """, (chat_id, after_id, current_user["id"], history_limit))
            messages = cursor.fetchall()
            has_more_after = len(messages) > limit
            messages = messages[:limit]
            has_more = False
        elif before_id:
            cursor.execute(f"""
                SELECT messages.id, messages.sender_id, messages.content, messages.timestamp,
                       messages.audio_duration, messages.audio_waveform,
                       messages.forwarded_from_message_id,
                       messages.forwarded_from_sender_id,
                       messages.forwarded_from_sender_name,
                       messages.forwarded_from_sender_username,
                       COALESCE(users.display_name, messages.sender_name) AS sender,
                       users.username AS sender_username,
                       messages.reply_to, messages.reactions, users.avatar_url, messages.read_by,
                       messages.edited_at, messages.delivery_error, messages.undelivered_to
                FROM messages
                LEFT JOIN users ON messages.sender_id = users.id
                WHERE messages.chat_id = ? AND messages.id < ?
                  AND {not_deleted_sql}
                ORDER BY messages.id DESC
                LIMIT ?
            """, (chat_id, before_id, current_user["id"], history_limit))
            messages = cursor.fetchall()
            has_more = len(messages) > limit
            has_more_before = has_more
            messages = list(reversed(messages[:limit]))
        else:
            cursor.execute(f"""
                SELECT messages.id, messages.sender_id, messages.content, messages.timestamp,
                       messages.audio_duration, messages.audio_waveform,
                       messages.forwarded_from_message_id,
                       messages.forwarded_from_sender_id,
                       messages.forwarded_from_sender_name,
                       messages.forwarded_from_sender_username,
                       COALESCE(users.display_name, messages.sender_name) AS sender,
                       users.username AS sender_username,
                       messages.reply_to, messages.reactions, users.avatar_url, messages.read_by,
                       messages.edited_at, messages.delivery_error, messages.undelivered_to
                FROM messages
                LEFT JOIN users ON messages.sender_id = users.id
                WHERE messages.chat_id = ?
                  AND {not_deleted_sql}
                ORDER BY messages.id DESC
                LIMIT ?
            """, (chat_id, current_user["id"], history_limit))
            messages = cursor.fetchall()
            has_more = len(messages) > limit
            has_more_before = has_more
            messages = list(reversed(messages[:limit]))
        logger.info(f"Fetched {len(messages)} messages for chat {chat_id}")

        history = []
        for msg in messages:
            try:
                if not _message_visible_to(msg, current_user["id"]):
                    continue
                content = msg["content"]
                message_type = "message"
                parsed_content = content
                if content and content.startswith("{"):
                    try:
                        parsed_content = json.loads(content)
                        if isinstance(parsed_content, dict) and "file_url" in parsed_content:
                            parsed_content = _with_audio_metadata(parsed_content, msg)
                            message_type = "file"
                    except json.JSONDecodeError as json_err:
                        logger.error(f"Failed to parse JSON content for message {msg['id']}: {type(json_err).__name__}")
                        parsed_content = content  # Keep as string if JSON is invalid
                        message_type = "message"

                sender_snapshot = serialize_user_snapshot(cursor, msg["sender_id"], current_user["id"])
                history.append({
                    "id": msg["id"],
                    "sender_id": msg["sender_id"],
                    "content": parsed_content,
                    "timestamp": to_utc_iso(msg["timestamp"]),
                    "sender": sender_snapshot["display_name"] or msg["sender"],
                    "sender_username": msg["sender_username"],
                    "avatar_url": sender_snapshot["avatar_url"],
                    "reply_to": msg["reply_to"],
                    "edited_at": to_utc_iso(msg["edited_at"]) if msg["edited_at"] else None,
                    "reactions": _hydrate_user_items(cursor, _parse_json_list(msg["reactions"]), current_user["id"]),
                    "read_by": _hydrate_user_items(cursor, _parse_json_list(msg["read_by"]), current_user["id"], respect_read_receipts=True),
                    "is_deleted": not bool(msg["content"]),
                    "type": message_type,
                    "forwarded_from": _forwarded_from_from_row(msg),
                    "delivery_error": msg["delivery_error"]
                })
            except Exception as e:
                logger.error(f"Error processing message {msg['id']} in chat {chat_id}: {str(e)}")
                continue  # Skip problematic message

        next_before_id = history[0]["id"] if has_more_before and history else None
        next_after_id = history[-1]["id"] if has_more_after and history else None
        logger.info(f"Returning {len(history)} messages for chat {chat_id}")
        return {
            "history": history,
            "has_more": has_more,
            "has_more_before": has_more_before,
            "has_more_after": has_more_after,
            "next_before_id": next_before_id,
            "next_after_id": next_after_id
        }
    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"Error loading history for chat {chat_id}: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error loading history: {str(e)}")
    finally:
        conn.close()    

@router.post("/{message_id}/delete-for-me")
async def delete_message_for_me(
    message_id: int,
    current_user: dict = Depends(get_current_user)
):
    """Soft-delete a message for the current user only. Other users can still see the message."""
    conn = get_connection()
    cursor = conn.cursor()

    try:
        # Fetch the message
        cursor.execute("""
            SELECT messages.id, messages.chat_id, messages.sender_id, messages.content,
                   messages.undelivered_to, messages.deleted_for, chats.type AS chat_type
            FROM messages
            LEFT JOIN chats ON messages.chat_id = chats.id
            WHERE messages.id = ?
        """, (message_id,))
        message = cursor.fetchone()
        
        if not message:
            raise HTTPException(status_code=404, detail="Message not found")

        # Check if user is member of the chat
        cursor.execute(
            "SELECT 1 FROM participants WHERE chat_id = ? AND user_id = ?",
            (message["chat_id"], current_user["id"])
        )
        if not cursor.fetchone():
            raise HTTPException(status_code=403, detail="You are not a member of this chat")

        user_id = current_user["id"]
        chat_id = message["chat_id"]
        deleted_for = deleted_for_user_ids(message)

        # Repeating the request is a no-op for messages the user already removed
        if user_id not in deleted_for:
            if not _message_visible_to(message, user_id):
                raise HTTPException(status_code=404, detail="Message not found")

            deleted_for.append(user_id)
            cursor.execute("""
                UPDATE messages
                SET deleted_for = ?
                WHERE id = ?
            """, (json.dumps(deleted_for), message_id))
            conn.commit()

        # Only the requesting user's sockets are told to drop the message; other
        # participants keep seeing it. Clients simply handle a regular delete event.
        await manager.broadcast_personalized(chat_id, lambda recipient_id: {
            "type": "delete",
            "chat_id": chat_id,
            "message_id": message_id,
            "timestamp": utc_now_iso(),
        } if recipient_id == user_id else None)
        await manager.broadcast_personalized(0, lambda recipient_id: {
            "type": "chat_list_delete",
            "chat_id": chat_id,
            "message_id": message_id,
            **get_chat_unread_summary(cursor, chat_id, user_id),
        } if recipient_id == user_id else None)

        return {"message": "Message deleted for you"}

    except HTTPException:
        raise
    except Exception as e:
        conn.rollback()
        logger.error(f"Error deleting message {message_id} for user {current_user['id']}: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error deleting message: {str(e)}")
    finally:
        conn.close()

# @router.put("/edit/{message_id}")
# def edit_message(message_id: int, payload: MessageEdit, current_user: dict = Depends(get_current_user)):
#     content = payload.content.strip()
#     if not content:
#         raise HTTPException(status_code=400, detail="Message text is required")

#     conn = get_connection()
#     cursor = conn.cursor()

#     try:
#         cursor.execute("SELECT sender_id, content FROM messages WHERE id = ?", (message_id,))
#         message = cursor.fetchone()
#         if not message:
#             raise HTTPException(status_code=404, detail="Message not found")
#         if message["sender_id"] != current_user["id"]:
#             raise HTTPException(status_code=403, detail="You are not the author of this message")
        
#         try:
#             content_data = json.loads(message["content"])
#             if "file_url" in content_data:
#                 raise HTTPException(status_code=400, detail="File messages cannot be edited")
#         except json.JSONDecodeError:
#             pass

#         cursor.execute("""
#             UPDATE messages
#             SET content = ?, edited_at = CURRENT_TIMESTAMP
#             WHERE id = ?
#         """, (content, message_id))
#         conn.commit()
#         return {"message": "Message successfully updated"}
#     except HTTPException:
#         raise
#     except Exception as e:
#         conn.rollback()
#         logger.error(f"Error editing message {message_id}: {str(e)}")
#         raise HTTPException(status_code=500, detail=f"Internal server error: {str(e)}")
#     finally:
#         conn.close()

# @router.delete("/delete/{message_id}")
# def delete_message(message_id: int, current_user: dict = Depends(get_current_user)):
#     conn = get_connection()
#     cursor = conn.cursor()

#     try:
#         cursor.execute("SELECT sender_id FROM messages WHERE id = ?", (message_id,))
#         message = cursor.fetchone()
#         if not message:
#             raise HTTPException(status_code=404, detail="Message not found")
#         if message["sender_id"] != current_user["id"]:
#             raise HTTPException(status_code=403, detail="You are not the author of this message")

#         cursor.execute("DELETE FROM messages WHERE id = ?", (message_id,))
#         conn.commit()
#         return {"message": "Message deleted"}
#     except Exception as e:
#         conn.rollback()
#         logger.error(f"Error deleting message {message_id}: {str(e)}")
#         raise HTTPException(status_code=500, detail=f"Internal server error: {str(e)}")
#     finally:
#         conn.close()
