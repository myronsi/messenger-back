from fastapi import WebSocket, WebSocketDisconnect, Query
from fastapi.routing import APIRouter
from starlette.websockets import WebSocketState
from server.database import get_connection
from server.routes.auth import verify_token
from server.presence import mark_user_connected, mark_user_disconnected, utc_now_iso
from server.chat_summary import get_message_summary
from server.privacy import DEFAULT_AVATAR, can_send_to_chat, read_receipts_enabled, serialize_user_snapshot, serialize_user
import logging
import sqlite3
import json

router = APIRouter()

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

GROUP_ROLES_WITH_MESSAGE_MODERATION = {"owner", "admin", "moderator"}


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
            logger.info(f"Broadcasting to chat {chat_id}: {message}, clients: {len(self.active_chats[chat_id])}")
            for websocket in list(self.active_chats[chat_id]):
                if websocket.application_state != WebSocketState.CONNECTED:
                    self.disconnect(chat_id, websocket)
                    continue
                try:
                    await websocket.send_text(json.dumps(message))
                    logger.info(f"Sent message to client in chat {chat_id}")
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
        user_id = item.get("user_id")
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


async def broadcast_presence_update(cursor, user_id: int, username: str, is_online: bool, last_seen: str | None):
    cursor.execute("SELECT id, username, display_name, avatar_url, bio, last_seen FROM users WHERE id = ?", (user_id,))
    target_user = cursor.fetchone()
    if not target_user:
        return
    chat_ids = set(get_user_chat_ids(cursor, user_id))
    chat_ids.add(0)
    for active_chat_id in chat_ids:
        await manager.broadcast_personalized(active_chat_id, lambda recipient_id: {
            "type": "presence_update",
            "user_id": user_id,
            "username": username,
            "is_online": is_online if serialize_user(cursor, target_user, recipient_id)["is_online"] else False,
            "last_seen": serialize_user(cursor, target_user, recipient_id)["last_seen"],
        })


async def safe_close_websocket(websocket: WebSocket, code: int = 1000):
    if websocket.application_state != WebSocketState.CONNECTED:
        return
    try:
        await websocket.close(code=code)
    except RuntimeError as exc:
        logger.debug(f"WebSocket already closed while closing safely: {exc}")


@router.websocket("/ws/chat/{chat_id}")
async def websocket_endpoint(websocket: WebSocket, chat_id: int, token: str = Query(...)):
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
        logger.info(f"WebSocket CONNECTED for {username} in chat {chat_id}")

        try:
            while True:
                if websocket.application_state != WebSocketState.CONNECTED:
                    logger.info(f"WebSocket no longer connected for {username} in chat {chat_id}")
                    break
                try:
                    data = await websocket.receive_text()
                except RuntimeError as exc:
                    logger.info(f"WebSocket receive stopped for {username} in chat {chat_id}: {exc}")
                    break
                logger.info(f"Received message in chat {chat_id} from {username}: {data}")

                try:
                    parsed_data = json.loads(data)
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
                except (json.JSONDecodeError, KeyError) as e:
                    await websocket.send_text(json.dumps({"type": "error", "message": "Invalid message format"}))
                    logger.error(f"JSON parsing error: {e}")
                    continue

                if message_type == "message":
                    if not content or not content.strip():
                        err = {"type": "error", "message": "Empty message"}
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
                        logger.info(f"Message saved in db: {{'chat_id': {chat_id}, 'sender_name': '{display_name}', 'reply_to': {reply_to}}}, ID: {message_id}")
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
                    if undelivered_to:
                        await manager.send_to_user(user_id, {
                            "type": "chat_list_message",
                            "chat_id": chat_id,
                            "sender_id": user_id,
                            "last_message": get_message_summary(cursor, message_id, user_id),
                        })
                    else:
                        await manager.broadcast(0, {
                            "type": "chat_list_message",
                            "chat_id": chat_id,
                            "sender_id": user_id,
                            "last_message": get_message_summary(cursor, message_id),
                        })

                elif message_type == "file":
                    if not file_url or not file_name or not file_type or not file_size:
                        await websocket.send_text(json.dumps({"type": "error", "message": "Missing file metadata"}))
                        continue

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
                        conn.commit()
                        message_id = cursor.lastrowid
                        logger.info(f"File message saved in db: {{'chat_id': {chat_id}, 'sender_name': '{display_name}', 'file_url': '{file_url}'}}, ID: {message_id}")
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
                    if undelivered_to:
                        await manager.send_to_user(user_id, {
                            "type": "chat_list_message",
                            "chat_id": chat_id,
                            "sender_id": user_id,
                            "last_message": get_message_summary(cursor, message_id, user_id),
                        })
                    else:
                        await manager.broadcast(0, {
                            "type": "chat_list_message",
                            "chat_id": chat_id,
                            "sender_id": user_id,
                            "last_message": get_message_summary(cursor, message_id),
                        })

                elif message_type == "resend":
                    if not message_id:
                        await websocket.send_text(json.dumps({"type": "error", "message": "Missing message_id", "message_id": message_id}))
                        continue

                    cursor.execute("""
                        SELECT id, sender_id, sender_name, content, timestamp, reply_to, reactions, read_by, delivery_error
                        FROM messages
                        WHERE id = ? AND chat_id = ?
                    """, (message_id, chat_id))
                    message = cursor.fetchone()
                    if not message or message["sender_id"] != user_id:
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
                            "data": {
                                "chat_id": chat_id,
                                **(content_payload if message_kind == "file" else {"content": content_payload}),
                                "message_id": message_id,
                                "reply_to": message["reply_to"],
                            },
                            "timestamp": timestamp,
                        }

                    await manager.broadcast_personalized(chat_id, build_resend_message)
                    await manager.broadcast(0, {
                        "type": "chat_list_message",
                        "chat_id": chat_id,
                        "sender_id": user_id,
                        "last_message": get_message_summary(cursor, message_id),
                    })

                elif message_type == "edit":
                    if not message_id or not content:
                        await websocket.send_text(json.dumps({"type": "error", "message": "Missing message_id or content"}))
                        continue

                    try:
                        cursor.execute("SELECT sender_id FROM messages WHERE id = ? AND chat_id = ?", (message_id, chat_id))
                        sender = cursor.fetchone()
                        if not sender or sender["sender_id"] != user_id:
                            await websocket.send_text(json.dumps({"type": "error", "message": "You are not the author of this message"}))
                            continue

                        timestamp = utc_now_iso()
                        cursor.execute("UPDATE messages SET content = ?, edited_at = ? WHERE id = ?", (content, timestamp, message_id))
                        conn.commit()
                        logger.info(f"Message edited: {{'message_id': {message_id}}}")
                    except sqlite3.Error as e:
                        logger.error(f"Error while editing message: {e}")
                        await websocket.send_text(json.dumps({"type": "error", "message": "Failed to edit message"}))
                        continue

                    await manager.broadcast(chat_id, {
                        "type": "edit",
                        "message_id": message_id,
                        "new_content": content,
                        "timestamp": timestamp,
                    })

                elif message_type == "delete":
                    if not message_id:
                        await websocket.send_text(json.dumps({"type": "error", "message": "Missing message_id"}))
                        continue

                    try:
                        cursor.execute("SELECT sender_id FROM messages WHERE id = ? AND chat_id = ?", (message_id, chat_id))
                        message = cursor.fetchone()
                        if not message or not _can_delete_message(cursor, chat_id, message["sender_id"], user_id):
                            await websocket.send_text(json.dumps({"type": "error", "message": "You do not have permission to delete this message"}))
                            continue

                        cursor.execute("DELETE FROM messages WHERE id = ?", (message_id,))
                        conn.commit()
                        logger.info(f"Message deleted: {{'message_id': {message_id}}}")
                    except sqlite3.Error as e:
                        logger.error(f"Error while deleting message: {e}")
                        await websocket.send_text(json.dumps({"type": "error", "message": "Failed to delete message"}))
                        continue

                    await manager.broadcast(chat_id, {
                        "type": "delete",
                        "message_id": message_id,
                        "timestamp": utc_now_iso(),
                    })

                elif message_type == "reaction_add":
                    if not message_id or not reaction:
                        await websocket.send_text(json.dumps({"type": "error", "message": "Missing message_id or reaction"}))
                        continue

                    try:
                        cursor.execute("SELECT reactions FROM messages WHERE id = ? AND chat_id = ?", (message_id, chat_id))
                        result = cursor.fetchone()
                        if not result:
                            await websocket.send_text(json.dumps({"type": "error", "message": "Message not found"}))
                            continue

                        reactions = _hydrate_user_items(cursor, _parse_json_list(result["reactions"]), user_id)
                        if any(r.get("user_id") == user_id and r.get("reaction") == reaction for r in reactions):
                            await websocket.send_text(json.dumps({"type": "error", "message": "You already reacted with this reaction"}))
                            continue

                        reaction_item = {**_user_snapshot(cursor, user_id, user_id), "reaction": reaction}
                        reactions.append(reaction_item)
                        cursor.execute("UPDATE messages SET reactions = ? WHERE id = ?", (json.dumps(reactions), message_id))
                        conn.commit()
                        logger.info(f"Reaction added: {{'message_id': {message_id}, 'user_id': {user_id}, 'reaction': '{reaction}'}}")
                    except sqlite3.Error as e:
                        logger.error(f"Error while adding reaction: {e}")
                        await websocket.send_text(json.dumps({"type": "error", "message": "Failed to add reaction"}))
                        continue

                    await manager.broadcast_personalized(chat_id, lambda recipient_id: {
                        "type": "reaction_add",
                        "message_id": message_id,
                        **{**_user_snapshot(cursor, user_id, recipient_id), "reaction": reaction},
                        "timestamp": utc_now_iso(),
                    })

                elif message_type == "reaction_remove":
                    if not message_id or not reaction:
                        await websocket.send_text(json.dumps({"type": "error", "message": "Missing message_id or reaction"}))
                        continue

                    try:
                        cursor.execute("SELECT reactions FROM messages WHERE id = ? AND chat_id = ?", (message_id, chat_id))
                        result = cursor.fetchone()
                        if not result:
                            await websocket.send_text(json.dumps({"type": "error", "message": "Message not found"}))
                            continue

                        reactions = _hydrate_user_items(cursor, _parse_json_list(result["reactions"]), user_id)
                        if not any(r.get("user_id") == user_id and r.get("reaction") == reaction for r in reactions):
                            await websocket.send_text(json.dumps({"type": "error", "message": "You cannot remove this reaction"}))
                            continue

                        new_reactions = [r for r in reactions if not (r.get("user_id") == user_id and r.get("reaction") == reaction)]
                        cursor.execute("UPDATE messages SET reactions = ? WHERE id = ?", (json.dumps(new_reactions), message_id))
                        conn.commit()
                        logger.info(f"Reaction removed: {{'message_id': {message_id}, 'user_id': {user_id}, 'reaction': '{reaction}'}}")
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
                    })

                elif message_type == "is_read":
                    if not message_id:
                        await websocket.send_text(json.dumps({"type": "error", "message": "Missing message_id"}))
                        continue

                    try:
                        cursor.execute("SELECT id, sender_id, read_by FROM messages WHERE id = ? AND chat_id = ?", (message_id, chat_id))
                        message = cursor.fetchone()
                        if not message:
                            await websocket.send_text(json.dumps({"type": "error", "message": "Message not found"}))
                            continue

                        if message["sender_id"] == user_id:
                            await websocket.send_text(json.dumps({"type": "error", "message": "Cannot mark own message as read"}))
                            continue

                        read_by = _hydrate_user_items(cursor, _parse_json_list(message["read_by"]), user_id, respect_read_receipts=True)
                        if any(r.get("user_id") == user_id for r in read_by):
                            continue

                        timestamp = utc_now_iso()
                        receipt_is_public = read_receipts_enabled(cursor, user_id)
                        read_item = {**_user_snapshot(cursor, user_id, user_id), "read_at": timestamp}
                        if not receipt_is_public:
                            read_item["hidden"] = True
                        read_by.append(read_item)
                        cursor.execute("UPDATE messages SET read_by = ? WHERE id = ?", (json.dumps(read_by), message_id))
                        conn.commit()
                        logger.info(f"Message marked as read: {{'message_id': {message_id}, 'user_id': {user_id}}}")

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
                        })
                    await manager.send_to_user(user_id, {
                        "type": "chat_list_read",
                        "chat_id": chat_id,
                        "message_id": message_id,
                        "reader_user_id": user_id,
                        "timestamp": timestamp,
                    })

                elif message_type == "group_created":
                    if chat_id == 0:
                        logger.info(f"Received group_created for chat {parsed_data.get('chat_id')}")
                        await manager.broadcast(chat_id, parsed_data)

        except WebSocketDisconnect:
            logger.info(f"{username} DISCONNECTED from {chat_id}")
            await handle_disconnect()
        except Exception as e:
            logger.error(f"Unexpected error in WebSocket for {username} in chat {chat_id}: {e}")
            await handle_disconnect()
            await safe_close_websocket(websocket, code=1000)
    finally:
        await handle_disconnect()
        conn.close()
