from fastapi import WebSocket, WebSocketDisconnect, Query
from fastapi.routing import APIRouter
from starlette.websockets import WebSocketState
from server.database import get_connection
from server.routes.auth import verify_token, verify_ws_ticket
from server.config import ws_allow_query_token
from server.presence import mark_user_connected, mark_user_disconnected, utc_now_iso
from server.chat_summary import deleted_for_user_ids, get_chat_unread_summary, get_message_summary, message_visible_to
from server.media_access import attachment_metadata, attachment_path_from_url, attachment_url, find_attachment_source, record_attachment
from server.privacy import DEFAULT_AVATAR, can_send_to_chat, read_receipts_enabled, serialize_user_snapshot, serialize_user
import logging
import sqlite3
import json

router = APIRouter()

logger = logging.getLogger(__name__)

GROUP_ROLES_WITH_MESSAGE_MODERATION = {"owner", "admin", "moderator"}

# A frame is checked after it is received, so uvicorn's --ws-max-size is the real memory cap.
MAX_FRAME_CHARS = 64 * 1024
MAX_MESSAGE_CHARS = 10_000
MAX_REACTION_CHARS = 64
MAX_TEMP_ID_CHARS = 100
MAX_FILE_URL_CHARS = 1024
MAX_FILE_NAME_CHARS = 255
MAX_FILE_TYPE_CHARS = 100
WS_POLICY_VIOLATION = 1008


class ConnectionManager:
    def __init__(self):
        self.active_chats = {}  # { chat_id: [websockets] }
        self.websocket_users = {}

    async def connect(self, chat_id: int, websocket: WebSocket, user_id: int):
        if chat_id not in self.active_chats:
            self.active_chats[chat_id] = []
        self.active_chats[chat_id].append(websocket)
        self.websocket_users[websocket] = user_id
        logger.info(f"Connected to chat {chat_id}. Active connections: {len(self.active_chats[chat_id])}")

    def disconnect(self, chat_id: int, websocket: WebSocket):
        if chat_id in self.active_chats:
            if websocket not in self.active_chats[chat_id]:
                return
            self.active_chats[chat_id].remove(websocket)
            self.websocket_users.pop(websocket, None)
            if not self.active_chats[chat_id]:
                del self.active_chats[chat_id]
            logger.info(f"Disconnected from chat {chat_id}. Active connections: {len(self.active_chats.get(chat_id, []))}")

    async def broadcast(self, chat_id: int, message: dict):
        if chat_id in self.active_chats:
            logger.info(f"Broadcasting {message.get('type')} to chat {chat_id}, clients: {len(self.active_chats[chat_id])}")
            for websocket in list(self.active_chats[chat_id]):
                if websocket.application_state != WebSocketState.CONNECTED:
                    self.disconnect(chat_id, websocket)
                    continue
                try:
                    await websocket.send_text(json.dumps(message))
                except Exception as e:
                    self.disconnect(chat_id, websocket)
                    logger.error(f"Error broadcasting to chat {chat_id}: {e}")

    async def broadcast_to_chat(self, chat_id: int, message: dict):
        await self.broadcast(chat_id, message)

    async def broadcast_personalized(self, chat_id: int, build_message):
        if chat_id in self.active_chats:
            for websocket in list(self.active_chats[chat_id]):
                if websocket.application_state != WebSocketState.CONNECTED:
                    self.disconnect(chat_id, websocket)
                    continue
                try:
                    recipient_id = self.websocket_users.get(websocket)
                    message = build_message(recipient_id)
                    if message is None:
                        continue
                    await websocket.send_text(json.dumps(message))
                except Exception as e:
                    self.disconnect(chat_id, websocket)
                    logger.error(f"Error broadcasting personalized message to chat {chat_id}: {e}")

    async def _drop(self, chat_id: int, websocket: WebSocket):
        self.disconnect(chat_id, websocket)
        if websocket.application_state != WebSocketState.CONNECTED:
            return
        try:
            await websocket.close(code=WS_POLICY_VIOLATION)
        except Exception as e:
            logger.debug(f"WebSocket already closed while dropping it: {e}")

    async def close_user_sockets(self, user_id: int, chat_id: int | None = None):
        """Cut a user off from one chat (or from every chat) after they lose access to it."""
        for active_chat_id, sockets in list(self.active_chats.items()):
            if chat_id is not None and active_chat_id != chat_id:
                continue
            for websocket in list(sockets):
                if self.websocket_users.get(websocket) == user_id:
                    await self._drop(active_chat_id, websocket)

    async def close_chat_sockets(self, chat_id: int):
        for websocket in list(self.active_chats.get(chat_id, [])):
            await self._drop(chat_id, websocket)

    async def send_to_user(self, user_id: int, message: dict):
        for websocket, websocket_user_id in list(self.websocket_users.items()):
            if websocket_user_id != user_id:
                continue
            if websocket.application_state != WebSocketState.CONNECTED:
                for chat_id, sockets in list(self.active_chats.items()):
                    if websocket in sockets:
                        self.disconnect(chat_id, websocket)
                continue
            try:
                await websocket.send_text(json.dumps(message))
            except Exception as e:
                for chat_id, sockets in list(self.active_chats.items()):
                    if websocket in sockets:
                        self.disconnect(chat_id, websocket)
                logger.error(f"Error sending message to user {user_id}: {e}")


manager = ConnectionManager()


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


def _user_snapshot(cursor, user_id: int, requester_id: int | None = None):
    return serialize_user_snapshot(cursor, user_id, requester_id)


def _hydrate_user_items(cursor, items, requester_id: int | None = None, respect_read_receipts: bool = False):
    hydrated = []
    for item in items:
        if not isinstance(item, dict):
            continue
        user_id = item.get("user_id") or item.get("id")
        if not user_id:
            continue
        if item.get("hidden") and requester_id != user_id:
            continue
        if respect_read_receipts and requester_id != user_id and not read_receipts_enabled(cursor, user_id):
            continue
        snapshot = _user_snapshot(cursor, user_id, requester_id)
        hydrated.append({**item, **{key: item.get(key) or snapshot[key] for key in snapshot}})
    return hydrated


def _get_group_role(cursor, chat_id: int, user_id: int) -> str | None:
    cursor.execute("""
        SELECT c.type, p.role, g.admin_id
        FROM chats c
        JOIN participants p ON p.chat_id = c.id AND p.user_id = ?
        LEFT JOIN groups g ON g.chat_id = c.id
        WHERE c.id = ?
    """, (user_id, chat_id))
    row = cursor.fetchone()
    if not row or row["type"] != "group":
        return None
    if row["admin_id"] == user_id:
        return "owner"
    role = (row["role"] or "member").lower()
    return role if role in {"owner", "admin", "moderator", "member"} else "member"


def _can_delete_message(cursor, chat_id: int, sender_id: int, user_id: int) -> bool:
    if sender_id == user_id:
        return True
    role = _get_group_role(cursor, chat_id, user_id)
    return role in GROUP_ROLES_WITH_MESSAGE_MODERATION


def _is_int(value) -> bool:
    return isinstance(value, int) and not isinstance(value, bool)


def _text_ok(value, limit: int) -> bool:
    return value is None or (isinstance(value, str) and len(value) <= limit)


def _validate_event_fields(event: dict) -> str | None:
    """Return an error message if any client-controlled field has the wrong type or is oversized."""
    if not isinstance(event.get("type", "message"), str):
        return "Invalid message format"
    temp_id = event.get("client_temp_id")
    if temp_id is not None and not (_is_int(temp_id) or (isinstance(temp_id, str) and len(temp_id) <= MAX_TEMP_ID_CHARS)):
        return "Invalid message format"
    if not _text_ok(event.get("content"), MAX_MESSAGE_CHARS):
        return "Message is too long" if isinstance(event.get("content"), str) else "Invalid message format"
    for key in ("message_id", "reply_to"):
        if event.get(key) is not None and not _is_int(event[key]):
            return "Invalid message format"
    size = event.get("file_size")
    if size is not None and (isinstance(size, bool) or not isinstance(size, (int, float)) or size < 0):
        return "Invalid message format"
    limits = (("reaction", MAX_REACTION_CHARS), ("file_url", MAX_FILE_URL_CHARS),
              ("file_name", MAX_FILE_NAME_CHARS), ("file_type", MAX_FILE_TYPE_CHARS))
    if not all(_text_ok(event.get(key), limit) for key, limit in limits):
        return "Invalid message format"
    return None


def _looks_like_file_payload(content) -> bool:
    """Text that parses as an attachment descriptor would be rendered as a file, so only uploads may produce it."""
    if not isinstance(content, str) or not content.lstrip().startswith("{"):
        return False
    try:
        parsed = json.loads(content)
    except ValueError:
        return False
    return isinstance(parsed, dict) and "file_url" in parsed


def _reply_target_is_valid(cursor, chat_id: int, user_id: int, reply_to) -> bool:
    if reply_to is None:
        return True
    cursor.execute(
        "SELECT sender_id, undelivered_to, deleted_for FROM messages WHERE id = ? AND chat_id = ?",
        (reply_to, chat_id),
    )
    target = cursor.fetchone()
    return target is not None and message_visible_to(target, user_id)


def _is_chat_participant(cursor, chat_id: int, user_id: int | None) -> bool:
    if user_id is None:
        return False
    cursor.execute("SELECT 1 FROM participants WHERE chat_id = ? AND user_id = ?", (chat_id, user_id))
    return cursor.fetchone() is not None


def get_user_chat_ids(cursor, user_id: int) -> list[int]:
    cursor.execute("SELECT chat_id FROM participants WHERE user_id = ?", (user_id,))
    return [row["chat_id"] for row in cursor.fetchall()]


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


def get_chat_participant_ids(cursor, chat_id: int) -> set[int]:
    cursor.execute("SELECT user_id FROM participants WHERE chat_id = ?", (chat_id,))
    return {row["user_id"] for row in cursor.fetchall()}


def get_users_sharing_chat_with(cursor, user_id: int) -> set[int]:
    cursor.execute(
        """
        SELECT DISTINCT other.user_id
        FROM participants own
        JOIN participants other ON other.chat_id = own.chat_id
        WHERE own.user_id = ?
        """,
        (user_id,),
    )
    return {row["user_id"] for row in cursor.fetchall()} | {user_id}


async def send_chat_list_message(cursor, chat_id: int, sender_id: int, message_id: int):
    """Tell each member of the chat (on their chat-list socket only) about the new last message."""
    members = get_chat_participant_ids(cursor, chat_id)

    def build(recipient_id):
        if recipient_id not in members:
            return None
        last_message = get_message_summary(cursor, message_id, recipient_id)
        if last_message is None:
            return None
        return {
            "type": "chat_list_message",
            "chat_id": chat_id,
            "sender_id": sender_id,
            "last_message": last_message,
        }

    await manager.broadcast_personalized(0, build)


async def broadcast_to_chat_list(user_ids, message: dict):
    """Send an event only to the chat-list sockets of the given users."""
    allowed = set(user_ids)
    await manager.broadcast_personalized(0, lambda recipient_id: message if recipient_id in allowed else None)


async def broadcast_presence_update(cursor, user_id: int, username: str, is_online: bool, last_seen: str | None):
    cursor.execute("SELECT id, username, display_name, avatar_url, bio, last_seen FROM users WHERE id = ?", (user_id,))
    target_user = cursor.fetchone()
    if not target_user:
        return
    audience = get_users_sharing_chat_with(cursor, user_id)

    def build(recipient_id):
        if recipient_id not in audience:
            return None
        visible = serialize_user(cursor, target_user, recipient_id)
        return {
            "type": "presence_update",
            "user_id": user_id,
            "username": username,
            "is_online": is_online if visible["is_online"] else False,
            "last_seen": visible["last_seen"],
        }

    for active_chat_id in set(get_user_chat_ids(cursor, user_id)) | {0}:
        await manager.broadcast_personalized(active_chat_id, build)


async def safe_close_websocket(websocket: WebSocket, code: int = 1000):
    if websocket.application_state != WebSocketState.CONNECTED:
        return
    try:
        await websocket.close(code=code)
    except RuntimeError as exc:
        logger.debug(f"WebSocket already closed while closing safely: {exc}")


@router.websocket("/ws/chat/{chat_id}")
async def websocket_endpoint(
    websocket: WebSocket,
    chat_id: int,
    ticket: str | None = Query(None),
    token: str | None = Query(None),
):
    user = None
    if ticket:
        user = verify_ws_ticket(ticket)
    elif token and ws_allow_query_token():
        user = verify_token(token)
    if not user:
        await websocket.accept()
        await websocket.send_text(json.dumps({"type": "error", "message": "Invalid token"}))
        await websocket.close(code=1008)
        return

    username = user["username"]
    display_name = user["display_name"]
    user_id = user["id"]

    conn = get_connection()
    cursor = conn.cursor()
    connected_to_manager = False

    async def handle_disconnect():
        nonlocal connected_to_manager
        if not connected_to_manager:
            return
        manager.disconnect(chat_id, websocket)
        connected_to_manager = False
        if mark_user_disconnected(user_id):
            last_seen = utc_now_iso()
            cursor.execute("UPDATE users SET last_seen = ? WHERE id = ?", (last_seen, user_id))
            conn.commit()
            await broadcast_presence_update(cursor, user_id, username, False, last_seen)

    try:
        cursor.execute("SELECT id FROM users WHERE id = ?", (user_id,))
        if not cursor.fetchone():
            await websocket.accept()
            await websocket.send_text(json.dumps({"type": "error", "message": "Account does not exist"}))
            await websocket.close(code=1008)
            return

        await websocket.accept()

        if chat_id != 0:
            cursor.execute("SELECT id FROM chats WHERE id = ?", (chat_id,))
            if not cursor.fetchone():
                await websocket.send_text(json.dumps({"type": "error", "message": "Chat does not exist"}))
                await websocket.close(code=1008)
                logger.error(f"Chat {chat_id} does not exist")
                return

            cursor.execute("SELECT * FROM participants WHERE chat_id = ? AND user_id = ?", (chat_id, user_id))
            participant = cursor.fetchone()
            if not participant:
                await websocket.send_text(json.dumps({"type": "error", "message": "You are not a member of this chat"}))
                await websocket.close(code=1008)
                logger.error(f"User {user_id} not found in participants for chat {chat_id}")
                return
            logger.info(f"User {user_id} verified as participant in chat {chat_id}")

        user_info = _user_snapshot(cursor, user_id, user_id)
        avatar_url = user_info["avatar_url"]

        await manager.connect(chat_id, websocket, user_id)
        connected_to_manager = True
        if mark_user_connected(user_id):
            await broadcast_presence_update(cursor, user_id, username, True, user.get("last_seen"))
        logger.info(f"WebSocket connected: user_id={user_id}, chat_id={chat_id}")

        try:
            while True:
                if websocket.application_state != WebSocketState.CONNECTED:
                    logger.info(f"WebSocket no longer connected: user_id={user_id}, chat_id={chat_id}")
                    break
                try:
                    data = await websocket.receive_text()
                except RuntimeError as exc:
                    logger.info(f"WebSocket receive stopped: user_id={user_id}, chat_id={chat_id}: {exc}")
                    break

                if len(data) > MAX_FRAME_CHARS:
                    await websocket.send_text(json.dumps({"type": "error", "message": "Message is too large"}))
                    continue

                try:
                    parsed_data = json.loads(data)
                    if not isinstance(parsed_data, dict):
                        raise ValueError("Event must be a JSON object")
                    field_error = _validate_event_fields(parsed_data)
                    if field_error:
                        await websocket.send_text(json.dumps({"type": "error", "message": field_error}))
                        continue
                    message_type = parsed_data.get("type", "message")
                    client_temp_id = parsed_data.get("client_temp_id")
                    content = parsed_data.get("content")
                    message_id = parsed_data.get("message_id")
                    reply_to = parsed_data.get("reply_to")
                    file_url = parsed_data.get("file_url")
                    file_name = parsed_data.get("file_name")
                    file_type = parsed_data.get("file_type")
                    file_size = parsed_data.get("file_size")
                    reaction = parsed_data.get("reaction")
                    logger.info(f"Received {message_type!r} event: user_id={user_id}, chat_id={chat_id}, message_id={message_id}")
                except (ValueError, KeyError) as e:
                    await websocket.send_text(json.dumps({"type": "error", "message": "Invalid message format"}))
                    logger.error(f"JSON parsing error: {e}")
                    continue

                # The chat-list socket (chat 0) is receive-only: clients must not push events through it
                if chat_id == 0:
                    await websocket.send_text(json.dumps({"type": "error", "message": "This connection is read-only"}))
                    continue

                # Membership can change while the socket stays open (removed, left, chat deleted)
                if not _is_chat_participant(cursor, chat_id, user_id):
                    logger.info(f"User {user_id} lost access to chat {chat_id}; closing socket")
                    await websocket.send_text(json.dumps({"type": "error", "message": "You are not a member of this chat"}))
                    await handle_disconnect()
                    await safe_close_websocket(websocket, code=WS_POLICY_VIOLATION)
                    break

                if message_type == "message":
                    if not content or not content.strip():
                        err = {"type": "error", "message": "Empty message"}
                        if client_temp_id is not None:
                            err["client_temp_id"] = client_temp_id
                        await websocket.send_text(json.dumps(err))
                        continue

                    if _looks_like_file_payload(content) or not _reply_target_is_valid(cursor, chat_id, user_id, reply_to):
                        err = {"type": "error", "message": "Invalid message"}
                        if client_temp_id is not None:
                            err["client_temp_id"] = client_temp_id
                        await websocket.send_text(json.dumps(err))
                        continue

                    undelivered_to, delivery_error = _undelivered_recipients_for_send(cursor, chat_id, user_id)
                    timestamp = utc_now_iso()
                    try:
                        cursor.execute("""
                            INSERT INTO messages (chat_id, sender_id, sender_name, content, timestamp, reply_to, delivery_error, undelivered_to)
                            VALUES (?, ?, ?, ?, ?, ?, ?, ?)
                        """, (chat_id, user_id, display_name, content, timestamp, reply_to, delivery_error, json.dumps(undelivered_to)))
                        conn.commit()
                        message_id = cursor.lastrowid
                        logger.info(f"Message saved: chat_id={chat_id}, message_id={message_id}, user_id={user_id}")
                    except sqlite3.Error as e:
                        logger.error(f"Error while saving message to db: {e}")
                        err = {"type": "error", "message": "Failed to save message"}
                        if client_temp_id is not None:
                            err["client_temp_id"] = client_temp_id
                        await websocket.send_text(json.dumps(err))
                        continue

                    def build_message(recipient_id):
                        if recipient_id in undelivered_to:
                            return None
                        sender_snapshot = _user_snapshot(cursor, user_id, recipient_id)
                        return {
                        "type": "message",
                        "username": sender_snapshot["display_name"],
                        "sender_username": username,
                        "sender_id": user_id,
                        "avatar_url": sender_snapshot["avatar_url"],
                        "is_deleted": False,
                        "delivery_error": delivery_error if recipient_id == user_id else None,
                        "reactions": [],
                        "read_by": [],
                        "data": {
                            "chat_id": chat_id,
                            "content": content,
                            "message_id": message_id,
                            "client_temp_id": client_temp_id,
                            "reply_to": reply_to,
                        },
                        "timestamp": timestamp,
                    }
                    await manager.broadcast_personalized(chat_id, build_message)
                    await send_chat_list_message(cursor, chat_id, user_id, message_id)

                elif message_type == "file":
                    if not file_url or not file_name or not file_type or not file_size:
                        await websocket.send_text(json.dumps({"type": "error", "message": "Missing file metadata"}))
                        continue

                    # A client may only attach files it can already read, never someone else's upload.
                    attachment_path = attachment_path_from_url(file_url)
                    source = find_attachment_source(cursor, user_id, attachment_path) if attachment_path else None
                    if source is None or not _reply_target_is_valid(cursor, chat_id, user_id, reply_to):
                        err = {"type": "error", "message": "Invalid file"}
                        if client_temp_id is not None:
                            err["client_temp_id"] = client_temp_id
                        await websocket.send_text(json.dumps(err))
                        continue
                    file_url = attachment_url(attachment_path)
                    recorded = attachment_metadata(source)
                    file_name = recorded.get("file_name", file_name)
                    file_type = recorded.get("file_type", file_type)
                    file_size = recorded.get("file_size", file_size)

                    undelivered_to, delivery_error = _undelivered_recipients_for_send(cursor, chat_id, user_id)
                    timestamp = utc_now_iso()
                    try:
                        cursor.execute("""
                            INSERT INTO messages (chat_id, sender_id, sender_name, content, timestamp, reply_to, delivery_error, undelivered_to)
                            VALUES (?, ?, ?, ?, ?, ?, ?, ?)
                        """, (chat_id, user_id, display_name, json.dumps({
                            "file_url": file_url,
                            "file_name": file_name,
                            "file_type": file_type,
                            "file_size": file_size,
                        }), timestamp, reply_to, delivery_error, json.dumps(undelivered_to)))
                        message_id = cursor.lastrowid
                        record_attachment(cursor, message_id, file_url)
                        conn.commit()
                        logger.info(f"File message saved: chat_id={chat_id}, message_id={message_id}, user_id={user_id}")
                    except sqlite3.Error as e:
                        logger.error(f"Error while saving file message to db: {e}")
                        err = {"type": "error", "message": "Failed to save file message"}
                        if client_temp_id is not None:
                            err["client_temp_id"] = client_temp_id
                        await websocket.send_text(json.dumps(err))
                        continue

                    def build_file_message(recipient_id):
                        if recipient_id in undelivered_to:
                            return None
                        sender_snapshot = _user_snapshot(cursor, user_id, recipient_id)
                        return {
                        "type": "file",
                        "username": sender_snapshot["display_name"],
                        "sender_username": username,
                        "sender_id": user_id,
                        "avatar_url": sender_snapshot["avatar_url"],
                        "is_deleted": False,
                        "delivery_error": delivery_error if recipient_id == user_id else None,
                        "reactions": [],
                        "read_by": [],
                        "data": {
                            "chat_id": chat_id,
                            "file_url": file_url,
                            "file_name": file_name,
                            "file_type": file_type,
                            "file_size": file_size,
                            "message_id": message_id,
                            "client_temp_id": client_temp_id,
                            "reply_to": reply_to,
                        },
                        "timestamp": timestamp,
                    }
                    await manager.broadcast_personalized(chat_id, build_file_message)
                    await send_chat_list_message(cursor, chat_id, user_id, message_id)

                elif message_type == "resend":
                    if not message_id:
                        await websocket.send_text(json.dumps({"type": "error", "message": "Missing message_id", "message_id": message_id}))
                        continue

                    cursor.execute("""
                        SELECT id, sender_id, sender_name, content, timestamp, reply_to, reactions, read_by, delivery_error,
                               forwarded_from_message_id, forwarded_from_sender_id,
                               forwarded_from_sender_name, forwarded_from_sender_username, deleted_for
                        FROM messages
                        WHERE id = ? AND chat_id = ?
                    """, (message_id, chat_id))
                    message = cursor.fetchone()
                    if not message or message["sender_id"] != user_id or not message_visible_to(message, user_id):
                        await websocket.send_text(json.dumps({"type": "error", "message": "Message not found", "message_id": message_id}))
                        continue
                    if not message["delivery_error"]:
                        await websocket.send_text(json.dumps({"type": "error", "message": "Message is already delivered", "message_id": message_id}))
                        continue
                    if not can_send_to_chat(cursor, chat_id, user_id):
                        await websocket.send_text(json.dumps({
                            "type": "error",
                            "message": "This message could not be delivered due to the recipient's privacy settings.",
                            "message_id": message_id,
                        }))
                        continue

                    timestamp = utc_now_iso()
                    cursor.execute("""
                        UPDATE messages
                        SET delivery_error = NULL, undelivered_to = '[]', timestamp = ?
                        WHERE id = ?
                    """, (timestamp, message_id))
                    conn.commit()

                    content_payload = message["content"]
                    message_kind = "message"
                    try:
                        parsed_content = json.loads(content_payload)
                        if isinstance(parsed_content, dict) and "file_url" in parsed_content:
                            content_payload = parsed_content
                            message_kind = "file"
                    except (json.JSONDecodeError, TypeError):
                        pass

                    def build_resend_message(recipient_id):
                        sender_snapshot = _user_snapshot(cursor, user_id, recipient_id)
                        return {
                            "type": message_kind,
                            "username": sender_snapshot["display_name"],
                            "sender_username": username,
                            "sender_id": user_id,
                            "avatar_url": sender_snapshot["avatar_url"],
                            "is_deleted": False,
                            "delivery_error": None,
                            "reactions": _hydrate_user_items(cursor, _parse_json_list(message["reactions"]), recipient_id),
                            "read_by": _hydrate_user_items(cursor, _parse_json_list(message["read_by"]), recipient_id, respect_read_receipts=True),
                            "forwarded_from": ({
                                "message_id": message["forwarded_from_message_id"],
                                "sender_id": message["forwarded_from_sender_id"],
                                "sender_name": message["forwarded_from_sender_name"],
                                "sender_username": message["forwarded_from_sender_username"],
                            } if message["forwarded_from_message_id"] is not None else None),
                            "data": {
                                "chat_id": chat_id,
                                **(content_payload if message_kind == "file" else {"content": content_payload}),
                                "message_id": message_id,
                                "reply_to": message["reply_to"],
                            },
                            "timestamp": timestamp,
                        }

                    await manager.broadcast_personalized(chat_id, build_resend_message)
                    await send_chat_list_message(cursor, chat_id, user_id, message_id)

                elif message_type == "edit":
                    if not message_id or not content:
                        await websocket.send_text(json.dumps({"type": "error", "message": "Missing message_id or content"}))
                        continue

                    if _looks_like_file_payload(content):
                        await websocket.send_text(json.dumps({"type": "error", "message": "Invalid message"}))
                        continue

                    try:
                        cursor.execute("SELECT sender_id, deleted_for, content FROM messages WHERE id = ? AND chat_id = ?", (message_id, chat_id))
                        sender = cursor.fetchone()
                        if not sender or sender["sender_id"] != user_id or not message_visible_to(sender, user_id):
                            await websocket.send_text(json.dumps({"type": "error", "message": "You are not the author of this message"}))
                            continue
                        if _looks_like_file_payload(sender["content"]):
                            await websocket.send_text(json.dumps({"type": "error", "message": "File messages cannot be edited"}))
                            continue
                        deleted_for = deleted_for_user_ids(sender)

                        timestamp = utc_now_iso()
                        cursor.execute("UPDATE messages SET content = ?, edited_at = ? WHERE id = ?", (content, timestamp, message_id))
                        conn.commit()
                        logger.info(f"Message edited: chat_id={chat_id}, message_id={message_id}, user_id={user_id}")
                    except sqlite3.Error as e:
                        logger.error(f"Error while editing message: {e}")
                        await websocket.send_text(json.dumps({"type": "error", "message": "Failed to edit message"}))
                        continue

                    await manager.broadcast_personalized(chat_id, lambda recipient_id: (
                        {
                            "type": "edit",
                            "chat_id": chat_id,
                            "message_id": message_id,
                            "new_content": content,
                            "timestamp": timestamp,
                        }
                        if recipient_id not in deleted_for
                        else None
                    ))
                    await manager.broadcast_personalized(0, lambda recipient_id: (
                        {
                            "type": "edit",
                            "chat_id": chat_id,
                            "message_id": message_id,
                            "new_content": content,
                            "timestamp": timestamp,
                        }
                        if _is_chat_participant(cursor, chat_id, recipient_id)
                        and get_message_summary(cursor, message_id, recipient_id) is not None
                        else None
                    ))

                elif message_type == "delete":
                    if not message_id:
                        await websocket.send_text(json.dumps({"type": "error", "message": "Missing message_id"}))
                        continue

                    try:
                        cursor.execute("SELECT sender_id, deleted_for FROM messages WHERE id = ? AND chat_id = ?", (message_id, chat_id))
                        message = cursor.fetchone()
                        if (
                            not message
                            or not message_visible_to(message, user_id)
                            or not _can_delete_message(cursor, chat_id, message["sender_id"], user_id)
                        ):
                            await websocket.send_text(json.dumps({"type": "error", "message": "You do not have permission to delete this message"}))
                            continue

                        cursor.execute("DELETE FROM messages WHERE id = ?", (message_id,))
                        conn.commit()
                        logger.info(f"Message deleted: chat_id={chat_id}, message_id={message_id}, user_id={user_id}")
                    except sqlite3.Error as e:
                        logger.error(f"Error while deleting message: {e}")
                        await websocket.send_text(json.dumps({"type": "error", "message": "Failed to delete message"}))
                        continue

                    await manager.broadcast(chat_id, {
                        "type": "delete",
                        "chat_id": chat_id,
                        "message_id": message_id,
                        "timestamp": utc_now_iso(),
                    })
                    await manager.broadcast_personalized(0, lambda recipient_id: (
                        {
                            "type": "chat_list_delete",
                            "chat_id": chat_id,
                            "message_id": message_id,
                            **get_chat_unread_summary(cursor, chat_id, recipient_id),
                        }
                        if _is_chat_participant(cursor, chat_id, recipient_id)
                        else None
                    ))

                elif message_type == "reaction_add":
                    if not message_id or not reaction:
                        await websocket.send_text(json.dumps({"type": "error", "message": "Missing message_id or reaction"}))
                        continue

                    try:
                        cursor.execute("SELECT reactions, deleted_for FROM messages WHERE id = ? AND chat_id = ?", (message_id, chat_id))
                        result = cursor.fetchone()
                        if not result or user_id in deleted_for_user_ids(result):
                            await websocket.send_text(json.dumps({"type": "error", "message": "Message not found"}))
                            continue
                        deleted_for = deleted_for_user_ids(result)

                        reactions = _hydrate_user_items(cursor, _parse_json_list(result["reactions"]), user_id)
                        if any(r.get("user_id") == user_id and r.get("reaction") == reaction for r in reactions):
                            await websocket.send_text(json.dumps({"type": "error", "message": "You already reacted with this reaction"}))
                            continue

                        reaction_item = {**_user_snapshot(cursor, user_id, user_id), "reaction": reaction}
                        reactions.append(reaction_item)
                        cursor.execute("UPDATE messages SET reactions = ? WHERE id = ?", (json.dumps(reactions), message_id))
                        conn.commit()
                        logger.info(f"Reaction added: message_id={message_id}, user_id={user_id}")
                    except sqlite3.Error as e:
                        logger.error(f"Error while adding reaction: {e}")
                        await websocket.send_text(json.dumps({"type": "error", "message": "Failed to add reaction"}))
                        continue

                    await manager.broadcast_personalized(chat_id, lambda recipient_id: {
                        "type": "reaction_add",
                        "message_id": message_id,
                        **{**_user_snapshot(cursor, user_id, recipient_id), "reaction": reaction},
                        "timestamp": utc_now_iso(),
                    } if recipient_id not in deleted_for else None)

                elif message_type == "reaction_remove":
                    if not message_id or not reaction:
                        await websocket.send_text(json.dumps({"type": "error", "message": "Missing message_id or reaction"}))
                        continue

                    try:
                        cursor.execute("SELECT reactions, deleted_for FROM messages WHERE id = ? AND chat_id = ?", (message_id, chat_id))
                        result = cursor.fetchone()
                        if not result or user_id in deleted_for_user_ids(result):
                            await websocket.send_text(json.dumps({"type": "error", "message": "Message not found"}))
                            continue
                        deleted_for = deleted_for_user_ids(result)

                        reactions = _hydrate_user_items(cursor, _parse_json_list(result["reactions"]), user_id)
                        if not any(r.get("user_id") == user_id and r.get("reaction") == reaction for r in reactions):
                            await websocket.send_text(json.dumps({"type": "error", "message": "You cannot remove this reaction"}))
                            continue

                        new_reactions = [r for r in reactions if not (r.get("user_id") == user_id and r.get("reaction") == reaction)]
                        cursor.execute("UPDATE messages SET reactions = ? WHERE id = ?", (json.dumps(new_reactions), message_id))
                        conn.commit()
                        logger.info(f"Reaction removed: message_id={message_id}, user_id={user_id}")
                    except sqlite3.Error as e:
                        logger.error(f"Error while removing reaction: {e}")
                        await websocket.send_text(json.dumps({"type": "error", "message": "Failed to remove reaction"}))
                        continue

                    await manager.broadcast_personalized(chat_id, lambda recipient_id: {
                        "type": "reaction_remove",
                        "message_id": message_id,
                        **_user_snapshot(cursor, user_id, recipient_id),
                        "reaction": reaction,
                        "timestamp": utc_now_iso(),
                    } if recipient_id not in deleted_for else None)

                elif message_type == "is_read":
                    if not message_id:
                        await websocket.send_text(json.dumps({"type": "error", "message": "Missing message_id"}))
                        continue

                    try:
                        cursor.execute("SELECT id, sender_id, read_by, deleted_for FROM messages WHERE id = ? AND chat_id = ?", (message_id, chat_id))
                        message = cursor.fetchone()
                        if not message or user_id in deleted_for_user_ids(message):
                            await websocket.send_text(json.dumps({"type": "error", "message": "Message not found"}))
                            continue
                        deleted_for = deleted_for_user_ids(message)

                        if message["sender_id"] == user_id:
                            await websocket.send_text(json.dumps({"type": "error", "message": "Cannot mark own message as read"}))
                            continue

                        read_by = _hydrate_user_items(cursor, _parse_json_list(message["read_by"]), user_id, respect_read_receipts=True)
                        if any((r.get("user_id") or r.get("id")) == user_id for r in read_by):
                            continue

                        timestamp = utc_now_iso()
                        receipt_is_public = read_receipts_enabled(cursor, user_id)
                        read_item = {**_user_snapshot(cursor, user_id, user_id), "read_at": timestamp}
                        if not receipt_is_public:
                            read_item["hidden"] = True
                        read_by.append(read_item)
                        cursor.execute("UPDATE messages SET read_by = ? WHERE id = ?", (json.dumps(read_by), message_id))
                        conn.commit()
                        logger.info(f"Message marked as read: message_id={message_id}, user_id={user_id}")

                    except sqlite3.Error as e:
                        logger.error(f"Error while marking message as read: {e}")
                        await websocket.send_text(json.dumps({"type": "error", "message": "Failed to mark message as read"}))
                        continue

                    if receipt_is_public:
                        await manager.broadcast_personalized(chat_id, lambda recipient_id: {
                            "type": "is_read",
                            "message_id": message_id,
                            **_user_snapshot(cursor, user_id, recipient_id),
                            "read_at": timestamp,
                            "timestamp": timestamp,
                        } if recipient_id not in deleted_for else None)
                    await manager.send_to_user(user_id, {
                        "type": "chat_list_read",
                        "chat_id": chat_id,
                        "message_id": message_id,
                        "reader_user_id": user_id,
                        "timestamp": timestamp,
                    })

        except WebSocketDisconnect:
            logger.info(f"WebSocket disconnected: user_id={user_id}, chat_id={chat_id}")
            await handle_disconnect()
        except Exception as e:
            logger.error(f"Unexpected error in WebSocket: user_id={user_id}, chat_id={chat_id}: {e}")
            await handle_disconnect()
            await safe_close_websocket(websocket, code=1000)
    finally:
        await handle_disconnect()
        conn.close()
