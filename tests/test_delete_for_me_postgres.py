"""Server-side "delete for me" behaviour against a real PostgreSQL database.

Set TEST_DATABASE_URL to a disposable database (its tables are truncated), e.g.
    TEST_DATABASE_URL=postgresql://postgres:test@localhost:5432/messenger
"""
import asyncio
import json
import os
import queue
import threading
import unittest
from unittest.mock import patch

os.environ.setdefault("SECRET_KEY", "test-only-secret-key-not-for-production-use")
TEST_DATABASE_URL = os.environ.get("TEST_DATABASE_URL")


def run(coro):
    return asyncio.run(coro)


class PostgresFixture:
    @classmethod
    def setUpClass(cls):
        import server.database as database

        cls.database = database
        cls.original_url = database.DATABASE_URL
        database.DATABASE_URL = TEST_DATABASE_URL
        database.setup_database()

    @classmethod
    def tearDownClass(cls):
        cls.database.DATABASE_URL = cls.original_url

    def setUp(self):
        conn = self.database.get_connection()
        cursor = conn.cursor()
        cursor.execute("TRUNCATE messages, participants, chats, users RESTART IDENTITY CASCADE")
        self.alice = self.add_user(cursor, "alice")
        self.bob = self.add_user(cursor, "bob")
        cursor.execute("INSERT INTO chats (name, type) VALUES ('room', 'group')")
        self.chat_id = cursor.lastrowid
        for user_id in (self.alice, self.bob):
            cursor.execute("INSERT INTO participants (chat_id, user_id) VALUES (?, ?)", (self.chat_id, user_id))
        conn.commit()
        conn.close()

    @staticmethod
    def add_user(cursor, username):
        cursor.execute(
            "INSERT INTO users (username, display_name, password) VALUES (?, ?, 'x')",
            (username, username.title()),
        )
        return cursor.lastrowid

    def add_message(self, text, sender_id=None, content=None):
        conn = self.database.get_connection()
        cursor = conn.cursor()
        cursor.execute(
            "INSERT INTO messages (chat_id, sender_id, sender_name, content) VALUES (?, ?, 'sender', ?)",
            (self.chat_id, sender_id or self.bob, content if content is not None else text),
        )
        message_id = cursor.lastrowid
        conn.commit()
        conn.close()
        return message_id


@unittest.skipUnless(TEST_DATABASE_URL, "TEST_DATABASE_URL is not set")
class DeleteForMePostgresTests(PostgresFixture, unittest.TestCase):
    def setUp(self):
        super().setUp()
        self.broadcasts = []

        async def broadcast_personalized(chat_id, build_message):
            self.broadcasts.append((chat_id, {rid: build_message(rid) for rid in (self.alice, self.bob)}))

        patcher = patch("server.routes.messages.manager.broadcast_personalized", broadcast_personalized)
        patcher.start()
        self.addCleanup(patcher.stop)

    def delete_for(self, user_id, message_id):
        from server.routes.messages import delete_message_for_me

        return run(delete_message_for_me(message_id, {"id": user_id}))

    def history(self, user_id, limit=50, before_id=None, after_id=None, around_id=None):
        from server.routes.messages import get_message_history

        return run(
            get_message_history(
                self.chat_id,
                limit=limit,
                before_id=before_id,
                after_id=after_id,
                around_id=around_id,
                current_user={"id": user_id},
            )
        )

    def history_ids(self, user_id, **kwargs):
        return [message["id"] for message in self.history(user_id, **kwargs)["history"]]

    def test_history_excludes_messages_deleted_for_the_user_only(self):
        ids = [self.add_message(f"message {i}") for i in range(5)]
        self.delete_for(self.alice, ids[1])
        self.delete_for(self.alice, ids[3])

        alice_history = self.history(self.alice)["history"]
        self.assertEqual([m["id"] for m in alice_history], [ids[0], ids[2], ids[4]])
        self.assertTrue(all("deleted_for" not in message for message in alice_history))
        self.assertEqual(self.history_ids(self.bob), ids)

    def test_history_pagination_is_not_affected_by_hidden_messages(self):
        ids = [self.add_message(f"message {i}") for i in range(40)]
        hidden = set(ids[10:35])
        for message_id in hidden:
            self.delete_for(self.alice, message_id)
        expected = [message_id for message_id in ids if message_id not in hidden]

        collected = []
        page = self.history(self.alice, limit=5)
        collected = page["history"][:] and [m["id"] for m in page["history"]]
        while page["next_before_id"]:
            page = self.history(self.alice, limit=5, before_id=page["next_before_id"])
            collected = [m["id"] for m in page["history"]] + collected

        self.assertEqual(collected, expected)

    def test_history_around_and_after_skip_hidden_messages(self):
        ids = [self.add_message(f"message {i}") for i in range(10)]
        self.delete_for(self.alice, ids[4])
        self.delete_for(self.alice, ids[6])

        self.assertNotIn(ids[4], self.history_ids(self.alice, around_id=ids[5], limit=8))
        self.assertNotIn(ids[6], self.history_ids(self.alice, around_id=ids[5], limit=8))
        self.assertEqual(self.history_ids(self.alice, after_id=ids[3], limit=3), [ids[5], ids[7], ids[8]])

    def test_search_photos_and_audios_skip_hidden_messages(self):
        from server.routes.messages import get_chat_audios, get_chat_photos, search_chat_messages

        keep = self.add_message("needle keep")
        hide = self.add_message("needle hide")
        photo_keep = self.add_message(None, content=json.dumps({"file_url": "/a.png", "file_name": "a.png", "file_type": "image"}))
        photo_hide = self.add_message(None, content=json.dumps({"file_url": "/b.png", "file_name": "b.png", "file_type": "image"}))
        audio_keep = self.add_message(None, content=json.dumps({"file_url": "/a.mp3", "file_name": "a.mp3", "file_type": "audio"}))
        audio_hide = self.add_message(None, content=json.dumps({"file_url": "/b.mp3", "file_name": "b.mp3", "file_type": "audio"}))
        for message_id in (hide, photo_hide, audio_hide):
            self.delete_for(self.alice, message_id)

        user = {"id": self.alice}
        found = run(search_chat_messages(self.chat_id, q="needle", current_user=user))["results"]
        photos = run(get_chat_photos(self.chat_id, current_user=user))["photos"]
        audios = run(get_chat_audios(self.chat_id, current_user=user))["audios"]

        self.assertEqual([m["id"] for m in found], [keep])
        self.assertEqual([m["id"] for m in photos], [photo_keep])
        self.assertEqual([m["id"] for m in audios], [audio_keep])

        bob = {"id": self.bob}
        self.assertEqual(len(run(search_chat_messages(self.chat_id, q="needle", current_user=bob))["results"]), 2)

    def test_delete_for_me_notifies_only_the_requester_and_is_idempotent(self):
        message_id = self.add_message("hello")

        self.delete_for(self.alice, message_id)
        self.delete_for(self.alice, message_id)

        conn = self.database.get_connection()
        cursor = conn.cursor()
        cursor.execute("SELECT deleted_for FROM messages WHERE id = ?", (message_id,))
        self.assertEqual(json.loads(cursor.fetchone()["deleted_for"]), [self.alice])
        conn.close()

        room_chat_id, room_payloads = self.broadcasts[0]
        self.assertEqual(room_chat_id, self.chat_id)
        self.assertIsNone(room_payloads[self.bob])
        self.assertEqual(room_payloads[self.alice]["type"], "delete")
        self.assertEqual(room_payloads[self.alice]["message_id"], message_id)

        list_chat_id, list_payloads = self.broadcasts[1]
        self.assertEqual(list_chat_id, 0)
        self.assertIsNone(list_payloads[self.bob])
        self.assertEqual(list_payloads[self.alice]["type"], "chat_list_delete")
        self.assertIsNone(list_payloads[self.alice]["last_message"])

    def test_forwarding_a_message_deleted_for_me_is_rejected(self):
        from fastapi import HTTPException

        from server.routes.messages import ForwardMessageRequest, forward_message

        message_id = self.add_message("secret")
        self.delete_for(self.alice, message_id)

        with self.assertRaises(HTTPException) as raised:
            run(
                forward_message(
                    ForwardMessageRequest(source_message_id=message_id, target_chat_ids=[self.chat_id]),
                    {"id": self.alice, "display_name": "Alice", "username": "alice"},
                )
            )
        self.assertEqual(raised.exception.status_code, 403)


@unittest.skipUnless(TEST_DATABASE_URL, "TEST_DATABASE_URL is not set")
class DeleteForMeWebSocketTests(PostgresFixture, unittest.TestCase):
    """End to end: REST delete-for-me plus the chat websockets of two users."""

    def setUp(self):
        super().setUp()
        # The websocket handlers hold a transaction open per connection, so the
        # settings rows have to exist already or a second socket would block on them.
        conn = self.database.get_connection()
        cursor = conn.cursor()
        for user_id in (self.alice, self.bob):
            cursor.execute("INSERT INTO user_privacy_settings (user_id) VALUES (?)", (user_id,))
        conn.commit()
        conn.close()

        from fastapi import FastAPI
        from starlette.testclient import TestClient

        from server.routes.auth import get_current_user
        from server.routes.messages import router as messages_router
        from server.websocket import router as websocket_router

        users = {
            "alice-token": {"id": self.alice, "username": "alice", "display_name": "Alice", "avatar_url": None, "last_seen": None},
            "bob-token": {"id": self.bob, "username": "bob", "display_name": "Bob", "avatar_url": None, "last_seen": None},
        }
        patcher = patch("server.websocket.verify_token", side_effect=lambda token: users.get(token))
        patcher.start()
        self.addCleanup(patcher.stop)

        app = FastAPI()
        app.include_router(messages_router, prefix="/messages")
        app.include_router(websocket_router)
        app.dependency_overrides[get_current_user] = lambda: users["alice-token"]
        # Entering the client keeps one event loop for every socket, as in production
        self.client = TestClient(app)
        self.client.__enter__()
        self.addCleanup(self.client.__exit__, None, None, None)

    @staticmethod
    def receive(websocket, *types, timeout=5):
        """Next event of one of the given types, skipping unrelated events such as presence."""
        while True:
            result = queue.Queue()
            threading.Thread(target=lambda: result.put(websocket.receive_text()), daemon=True).start()
            event = json.loads(result.get(timeout=timeout))
            if event.get("type") in types:
                return event

    def test_only_the_requester_is_told_and_later_events_are_not_leaked(self):
        chat_path = f"/ws/chat/{self.chat_id}"
        with self.client.websocket_connect(f"{chat_path}?token=alice-token") as alice_room, \
                self.client.websocket_connect(f"{chat_path}?token=bob-token") as bob_room, \
                self.client.websocket_connect("/ws/chat/0?token=alice-token") as alice_list:
            bob_room.send_text(json.dumps({"type": "message", "content": "hello"}))
            message_id = self.receive(alice_room, "message")["data"]["message_id"]
            self.receive(bob_room, "message")
            self.receive(alice_list, "chat_list_message")

            response = self.client.post(f"/messages/{message_id}/delete-for-me")
            self.assertEqual(response.status_code, 200)

            removed = self.receive(alice_room, "delete")
            self.assertEqual((removed["chat_id"], removed["message_id"]), (self.chat_id, message_id))
            summary = self.receive(alice_list, "chat_list_delete")
            self.assertEqual(summary["chat_id"], self.chat_id)
            self.assertIsNone(summary["last_message"])
            self.assertEqual(summary["unread_count"], 0)

            # Alice may no longer act on the message she removed
            alice_room.send_text(json.dumps({"type": "reaction_add", "message_id": message_id, "reaction": "x"}))
            self.assertEqual(self.receive(alice_room, "error")["message"], "Message not found")
            alice_room.send_text(json.dumps({"type": "delete", "message_id": message_id}))
            self.assertIn("permission", self.receive(alice_room, "error")["message"])

            # Bob still can, and none of it reaches Alice
            bob_room.send_text(json.dumps({"type": "edit", "message_id": message_id, "content": "hello!"}))
            self.assertEqual(self.receive(bob_room, "edit")["new_content"], "hello!")
            bob_room.send_text(json.dumps({"type": "reaction_add", "message_id": message_id, "reaction": "x"}))
            self.assertEqual(self.receive(bob_room, "reaction_add")["message_id"], message_id)
            bob_room.send_text(json.dumps({"type": "message", "content": "ping"}))
            ping = self.receive(alice_room, "message", "edit", "reaction_add", "delete")
            self.assertEqual(ping["type"], "message")
            self.assertEqual(ping["data"]["content"], "ping")

        conn = self.database.get_connection()
        cursor = conn.cursor()
        cursor.execute("SELECT content, reactions FROM messages WHERE id = ?", (message_id,))
        row = cursor.fetchone()
        conn.close()
        self.assertEqual(row["content"], "hello!")
        self.assertEqual(len(json.loads(row["reactions"])), 1)


if __name__ == "__main__":
    unittest.main()
