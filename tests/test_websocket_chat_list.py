"""Chat-list websocket events must only reach members of the chat (MSGC-37).

Needs a disposable PostgreSQL database in TEST_DATABASE_URL (see test_delete_for_me_postgres).
"""
import json
import unittest
from unittest.mock import patch

from tests.test_delete_for_me_postgres import TEST_DATABASE_URL, PostgresFixture


@unittest.skipUnless(TEST_DATABASE_URL, "TEST_DATABASE_URL is not set")
class ChatListIsolationTests(PostgresFixture, unittest.TestCase):
    """alice and bob share a chat; carol and dave share another; carol is not in alice and bob's chat."""

    def setUp(self):
        super().setUp()
        conn = self.database.get_connection()
        cursor = conn.cursor()
        self.carol = self.add_user(cursor, "carol")
        self.dave = self.add_user(cursor, "dave")
        cursor.execute("INSERT INTO chats (name, type) VALUES ('other', 'group')")
        self.other_chat_id = cursor.lastrowid
        for user_id in (self.carol, self.dave):
            cursor.execute("INSERT INTO participants (chat_id, user_id) VALUES (?, ?)", (self.other_chat_id, user_id))
        # Settings rows must exist up front or concurrent sockets block on creating them
        for user_id in (self.alice, self.bob, self.carol, self.dave):
            cursor.execute("INSERT INTO user_privacy_settings (user_id) VALUES (?)", (user_id,))
        conn.commit()
        conn.close()

        from fastapi import FastAPI
        from starlette.testclient import TestClient

        from server.websocket import router as websocket_router

        users = {
            f"{name}-token": {
                "id": user_id, "username": name, "display_name": name.title(), "avatar_url": None, "last_seen": None,
            }
            for name, user_id in (("alice", self.alice), ("bob", self.bob), ("carol", self.carol), ("dave", self.dave))
        }
        patcher = patch("server.websocket.verify_token", side_effect=lambda token: users.get(token))
        patcher.start()
        self.addCleanup(patcher.stop)

        app = FastAPI()
        app.include_router(websocket_router)
        self.client = TestClient(app)
        self.client.__enter__()
        self.addCleanup(self.client.__exit__, None, None, None)

    @staticmethod
    def receive(websocket, *types, timeout=5):
        import queue
        import threading

        while True:
            result = queue.Queue()
            threading.Thread(target=lambda: result.put(websocket.receive_text()), daemon=True).start()
            event = json.loads(result.get(timeout=timeout))
            if event.get("type") in types:
                return event

    def attach_file(self, file_url):
        from server.media_access import record_attachment

        message_id = self.add_message("file", sender_id=self.bob, content=json.dumps({"file_url": file_url}))
        conn = self.database.get_connection()
        record_attachment(conn.cursor(), message_id, file_url)
        conn.commit()
        conn.close()

    def list_socket(self, name):
        return self.client.websocket_connect(f"/ws/chat/0?token={name}-token")

    def room_socket(self, name, chat_id):
        return self.client.websocket_connect(f"/ws/chat/{chat_id}?token={name}-token")

    def send(self, websocket, **payload):
        websocket.send_text(json.dumps(payload))

    def test_non_member_does_not_receive_messages_of_other_chats(self):
        with self.list_socket("carol") as carol_list, \
                self.list_socket("alice") as alice_list, \
                self.room_socket("bob", self.chat_id) as bob_room, \
                self.room_socket("dave", self.other_chat_id) as dave_room:
            self.send(bob_room, type="message", content="secret for alice")
            event = self.receive(alice_list, "chat_list_message")
            self.assertEqual(event["chat_id"], self.chat_id)
            self.assertEqual(event["last_message"]["content"], "secret for alice")

            self.attach_file("/static/uploads/x.png")
            self.send(bob_room, type="file", file_url="/static/uploads/x.png", file_name="x.png", file_type="image/png", file_size=1)
            self.assertEqual(self.receive(alice_list, "chat_list_message")["last_message"]["type"], "file")

            # Carol's list gets her own chat's event next, never one from alice and bob's chat
            self.send(dave_room, type="message", content="hi carol")
            event = self.receive(carol_list, "chat_list_message")
            self.assertEqual(event["chat_id"], self.other_chat_id)
            self.assertEqual(event["last_message"]["content"], "hi carol")

    def test_file_message_cannot_reference_a_file_the_sender_cannot_read(self):
        # The file belongs to carol and dave's chat, so bob may not attach it to his own chat
        conn = self.database.get_connection()
        cursor = conn.cursor()
        cursor.execute(
            "INSERT INTO messages (chat_id, sender_id, sender_name, content) VALUES (?, ?, 'carol', ?)",
            (self.other_chat_id, self.carol, json.dumps({"file_url": "/static/uploads/private.png"})),
        )
        from server.media_access import record_attachment

        record_attachment(cursor, cursor.lastrowid, "/static/uploads/private.png")
        conn.commit()
        conn.close()
        with self.room_socket("bob", self.chat_id) as bob_room:
            for file_url in ("/static/uploads/private.png", "https://example.com/x.png", "/static/uploads/../x"):
                self.send(bob_room, type="file", file_url=file_url, file_name="x.png", file_type="image/png", file_size=1)
                self.assertEqual(self.receive(bob_room, "error")["message"], "Invalid file")

    def test_sender_and_recipient_both_get_their_own_chat_list_update(self):
        with self.list_socket("alice") as alice_list, \
                self.list_socket("bob") as bob_list, \
                self.room_socket("bob", self.chat_id) as bob_room:
            self.send(bob_room, type="message", content="hello")
            for socket in (alice_list, bob_list):
                event = self.receive(socket, "chat_list_message")
                self.assertEqual((event["chat_id"], event["last_message"]["content"]), (self.chat_id, "hello"))

    def next_presence_from_others(self, websocket, own_username):
        while True:
            event = self.receive(websocket, "presence_update")
            if event["username"] != own_username:
                return event

    def test_presence_only_reaches_users_sharing_a_chat(self):
        with self.list_socket("carol") as carol_list, self.list_socket("bob") as bob_list:
            with self.list_socket("alice"):
                self.assertEqual(self.next_presence_from_others(bob_list, "bob")["username"], "alice")
                with self.list_socket("dave"):
                    # alice connected before dave, so dave must be the first other user carol hears about
                    event = self.next_presence_from_others(carol_list, "carol")
                    self.assertEqual(event["username"], "dave")
                    self.assertTrue(event["is_online"])

if __name__ == "__main__":
    unittest.main()
