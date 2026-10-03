"""WebSocket authorization (MSGC-38): tickets, per-event membership, reply_to, file metadata and size limits.

The socket tests need a disposable PostgreSQL database in TEST_DATABASE_URL (see test_delete_for_me_postgres).
"""
import json
import queue
import threading
import unittest
from unittest.mock import patch

from server.config import ws_allow_query_token
from server.ws_tickets import MAX_OUTSTANDING_PER_USER, WebSocketTicketStore
from tests.test_delete_for_me_postgres import TEST_DATABASE_URL, PostgresFixture


class TicketStoreTests(unittest.TestCase):
    def setUp(self):
        self.now = 1000.0
        self.store = WebSocketTicketStore(ttl_seconds=30, clock=lambda: self.now)

    def test_ticket_is_single_use(self):
        ticket = self.store.issue(7, "session-1")
        self.assertEqual(self.store.consume(ticket), (7, "session-1"))
        self.assertIsNone(self.store.consume(ticket))

    def test_ticket_expires(self):
        ticket = self.store.issue(7, "session-1")
        self.now += 31
        self.assertIsNone(self.store.consume(ticket))

    def test_unknown_or_malformed_tickets_are_rejected(self):
        self.assertIsNone(self.store.consume("nope"))
        self.assertIsNone(self.store.consume(""))
        self.assertIsNone(self.store.consume(None))

    def test_unused_tickets_per_user_are_capped_and_expired_ones_do_not_count(self):
        for _ in range(MAX_OUTSTANDING_PER_USER):
            self.assertIsNotNone(self.store.issue(7, "session-1"))
        self.assertIsNone(self.store.issue(7, "session-1"))
        self.assertIsNotNone(self.store.issue(8, "session-2"))
        self.now += 31
        self.assertIsNotNone(self.store.issue(7, "session-1"))

    def test_query_token_flag(self):
        self.assertTrue(ws_allow_query_token({}))
        self.assertTrue(ws_allow_query_token({"WS_ALLOW_QUERY_TOKEN": "true"}))
        for value in ("false", "0", "No", " off "):
            self.assertFalse(ws_allow_query_token({"WS_ALLOW_QUERY_TOKEN": value}))


@unittest.skipUnless(TEST_DATABASE_URL, "TEST_DATABASE_URL is not set")
class WebSocketAuthorizationTests(PostgresFixture, unittest.TestCase):
    """alice (owner) and bob share a group; carol is outside it."""

    def setUp(self):
        super().setUp()
        conn = self.database.get_connection()
        cursor = conn.cursor()
        self.carol = self.add_user(cursor, "carol")
        cursor.execute("INSERT INTO groups (chat_id, admin_id) VALUES (?, ?)", (self.chat_id, self.alice))
        cursor.execute("UPDATE participants SET role = 'owner' WHERE chat_id = ? AND user_id = ?", (self.chat_id, self.alice))
        cursor.execute("INSERT INTO chats (name, type) VALUES ('other', 'group')")
        self.other_chat_id = cursor.lastrowid
        cursor.execute("INSERT INTO participants (chat_id, user_id) VALUES (?, ?)", (self.other_chat_id, self.carol))
        for user_id in (self.alice, self.bob, self.carol):
            cursor.execute("INSERT INTO user_privacy_settings (user_id) VALUES (?)", (user_id,))
        conn.commit()
        conn.close()

        from fastapi import FastAPI
        from starlette.testclient import TestClient

        from server.routes import auth, groups
        from server.websocket import router as websocket_router

        self.users = {
            f"{name}-token": {"id": user_id, "username": name, "display_name": name.title(), "avatar_url": None, "last_seen": None}
            for name, user_id in (("alice", self.alice), ("bob", self.bob), ("carol", self.carol))
        }
        patcher = patch("server.websocket.verify_token", side_effect=lambda token: self.users.get(token))
        patcher.start()
        self.addCleanup(patcher.stop)

        app = FastAPI()
        app.include_router(websocket_router)
        app.include_router(groups.router, prefix="/groups")
        app.include_router(auth.router, prefix="/auth")
        self.current_user = self.users["alice-token"]
        app.dependency_overrides[auth.get_current_user] = lambda: {**self.current_user, "session_id": "s1"}
        self.client = TestClient(app)
        self.client.__enter__()
        self.addCleanup(self.client.__exit__, None, None, None)

    @staticmethod
    def receive(websocket, *types, timeout=5):
        while True:
            result = queue.Queue()

            def read():
                try:
                    result.put(websocket.receive_text())
                except Exception as exc:
                    result.put(exc)

            threading.Thread(target=read, daemon=True).start()
            received = result.get(timeout=timeout)
            if isinstance(received, Exception):
                raise received
            event = json.loads(received)
            if event.get("type") in types:
                return event

    def room(self, name, chat_id=None):
        return self.client.websocket_connect(f"/ws/chat/{chat_id or self.chat_id}?token={name}-token")

    def send(self, websocket, **payload):
        websocket.send_text(json.dumps(payload))

    def stored_messages(self, chat_id=None):
        conn = self.database.get_connection()
        cursor = conn.cursor()
        cursor.execute("SELECT content, reply_to FROM messages WHERE chat_id = ? ORDER BY id", (chat_id or self.chat_id,))
        rows = [dict(row) for row in cursor.fetchall()]
        conn.close()
        return rows

    def remove_member(self, user_id):
        conn = self.database.get_connection()
        conn.cursor().execute("DELETE FROM participants WHERE chat_id = ? AND user_id = ?", (self.chat_id, user_id))
        conn.commit()
        conn.close()

    def test_removed_member_cannot_send_on_an_open_socket(self):
        with self.room("bob") as bob:
            self.send(bob, type="message", content="before")
            self.assertEqual(self.receive(bob, "message")["data"]["content"], "before")
            self.remove_member(self.bob)
            self.send(bob, type="message", content="after removal")
            self.assertEqual(self.receive(bob, "error")["message"], "You are not a member of this chat")
        self.assertEqual([m["content"] for m in self.stored_messages()], ["before"])

    def test_leaving_a_group_closes_the_open_socket(self):
        from starlette.websockets import WebSocketDisconnect

        self.current_user = self.users["bob-token"]
        with self.room("bob") as bob, self.room("alice") as alice:
            response = self.client.delete(f"/groups/{self.chat_id}/leave")
            self.assertEqual(response.status_code, 200, response.text)
            with self.assertRaises(WebSocketDisconnect):
                while True:
                    self.receive(bob, "never")
            self.send(alice, type="message", content="only alice now")
            self.assertEqual(self.receive(alice, "message")["data"]["content"], "only alice now")

    def test_removing_a_participant_closes_their_socket(self):
        from starlette.websockets import WebSocketDisconnect

        with self.room("bob") as bob:
            response = self.client.delete(f"/groups/{self.chat_id}/participants/bob")
            self.assertEqual(response.status_code, 200, response.text)
            with self.assertRaises(WebSocketDisconnect):
                while True:
                    self.receive(bob, "never")

    def test_reply_to_must_be_a_visible_message_of_the_same_chat(self):
        own = self.add_message("hello", sender_id=self.alice)
        conn = self.database.get_connection()
        cursor = conn.cursor()
        cursor.execute("INSERT INTO messages (chat_id, sender_id, sender_name, content) VALUES (?, ?, 'carol', 'elsewhere')",
                       (self.other_chat_id, self.carol))
        foreign = cursor.lastrowid
        conn.commit()
        conn.close()
        with self.room("bob") as bob:
            for bad in (foreign, 999999):
                self.send(bob, type="message", content="reply", reply_to=bad, client_temp_id="t1")
                error = self.receive(bob, "error")
                self.assertEqual((error["message"], error["client_temp_id"]), ("Invalid message", "t1"))
            self.send(bob, type="message", content="reply", reply_to=own)
            self.assertEqual(self.receive(bob, "message")["data"]["reply_to"], own)
        self.assertEqual([(m["content"], m["reply_to"]) for m in self.stored_messages()][-1:], [("reply", own)])
        self.assertEqual(len(self.stored_messages()), 2)

    def test_text_cannot_masquerade_as_a_file_message(self):
        payload = json.dumps({"file_url": "https://evil.example/x.png", "file_name": "x", "file_type": "image/png", "file_size": 1})
        mine = self.add_message("plain", sender_id=self.bob)
        with self.room("bob") as bob:
            self.send(bob, type="message", content=payload)
            self.assertEqual(self.receive(bob, "error")["message"], "Invalid message")
            self.send(bob, type="edit", message_id=mine, content=payload)
            self.assertEqual(self.receive(bob, "error")["message"], "Invalid message")
        self.assertEqual([m["content"] for m in self.stored_messages()], ["plain"])

    def test_file_messages_cannot_be_edited(self):
        content = json.dumps({"file_url": "/static/uploads/a.png", "file_name": "a.png", "file_type": "image", "file_size": 3})
        file_message = self.add_message("file", sender_id=self.bob, content=content)
        with self.room("bob") as bob:
            self.send(bob, type="edit", message_id=file_message, content="now text")
            self.assertEqual(self.receive(bob, "error")["message"], "File messages cannot be edited")
        self.assertEqual(self.stored_messages()[0]["content"], content)

    def test_file_metadata_comes_from_the_original_upload(self):
        from server.media_access import record_attachment

        original = {"file_url": "/static/uploads/a.png", "file_name": "a.png", "file_type": "image", "file_size": 3}
        message_id = self.add_message("file", sender_id=self.bob, content=json.dumps(original))
        conn = self.database.get_connection()
        record_attachment(conn.cursor(), message_id, "/static/uploads/a.png")
        conn.commit()
        conn.close()
        with self.room("bob") as bob:
            self.send(bob, type="file", file_url="/static/uploads//a.png", file_name="invoice.exe", file_type="application", file_size=99999)
            event = self.receive(bob, "file")
        self.assertEqual({k: event["data"][k] for k in original}, original)
        self.assertEqual(json.loads(self.stored_messages()[-1]["content"]), original)

    def test_oversized_and_malformed_events_are_rejected_without_closing_the_socket(self):
        from server.websocket import MAX_FRAME_CHARS, MAX_MESSAGE_CHARS

        with self.room("bob") as bob:
            bob.send_text("x" * (MAX_FRAME_CHARS + 1))
            self.assertEqual(self.receive(bob, "error")["message"], "Message is too large")
            self.send(bob, type="message", content="y" * (MAX_MESSAGE_CHARS + 1))
            self.assertEqual(self.receive(bob, "error")["message"], "Message is too long")
            for bad in ("[1, 2]", '"text"', json.dumps({"type": "message", "content": ["a"]}),
                        json.dumps({"type": "delete", "message_id": "1"}), json.dumps({"type": "message", "content": "x", "reply_to": True})):
                bob.send_text(bad)
                self.assertEqual(self.receive(bob, "error")["message"], "Invalid message format")
            self.send(bob, type="message", content="still works")
            self.assertEqual(self.receive(bob, "message")["data"]["content"], "still works")
        self.assertEqual([m["content"] for m in self.stored_messages()], ["still works"])

    def test_ticket_authenticates_a_socket_once(self):
        from starlette.websockets import WebSocketDisconnect

        self.current_user = self.users["bob-token"]
        with patch("server.websocket.verify_ws_ticket", side_effect=lambda t: self.users["bob-token"] if t == "good" else None):
            with self.client.websocket_connect(f"/ws/chat/{self.chat_id}?ticket=good") as bob:
                self.send(bob, type="message", content="via ticket")
                self.assertEqual(self.receive(bob, "message")["data"]["content"], "via ticket")
            with self.client.websocket_connect(f"/ws/chat/{self.chat_id}?ticket=bad") as rejected:
                self.assertEqual(self.receive(rejected, "error")["message"], "Invalid token")

    def test_ws_ticket_endpoint_issues_a_redeemable_single_use_ticket(self):
        from server.routes.auth import verify_ws_ticket

        self.current_user = self.users["bob-token"]
        response = self.client.post("/auth/ws-ticket")
        self.assertEqual(response.status_code, 200, response.text)
        with patch("server.routes.auth._load_session_user", side_effect=lambda user_id, session_id: {"id": user_id, "session_id": session_id}):
            self.assertEqual(verify_ws_ticket(response.json()["ticket"]), {"id": self.bob, "session_id": "s1"})
            self.assertIsNone(verify_ws_ticket(response.json()["ticket"]))

    def test_query_token_can_be_disabled(self):
        with patch("server.websocket.ws_allow_query_token", return_value=False):
            with self.client.websocket_connect(f"/ws/chat/{self.chat_id}?token=bob-token") as bob:
                self.assertEqual(self.receive(bob, "error")["message"], "Invalid token")


if __name__ == "__main__":
    unittest.main()
