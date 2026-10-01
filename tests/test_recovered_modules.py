import asyncio
import os
import unittest
from unittest.mock import patch

os.environ.setdefault("SECRET_KEY", "test-only-secret-key-not-for-production-use")


class TimeUtilityTests(unittest.TestCase):
    def test_to_utc_iso_normalizes_naive_sqlite_timestamp(self):
        from server.time_utils import to_utc_iso

        self.assertEqual(to_utc_iso("2026-09-26 12:34:56"), "2026-09-26T12:34:56Z")


class PresenceTests(unittest.TestCase):
    def test_user_remains_online_until_all_connections_close(self):
        from server.presence import (
            is_user_online,
            mark_user_connected,
            mark_user_disconnected,
        )

        user_id = 999_999
        self.assertTrue(mark_user_connected(user_id))
        self.assertFalse(mark_user_connected(user_id))
        self.assertTrue(is_user_online(user_id))
        self.assertFalse(mark_user_disconnected(user_id))
        self.assertTrue(is_user_online(user_id))
        self.assertTrue(mark_user_disconnected(user_id))
        self.assertFalse(is_user_online(user_id))


class AvatarUrlTests(unittest.TestCase):
    def test_avatar_url_escapes_path_segments(self):
        from server.avatar_history import make_avatar_url

        self.assertEqual(
            make_avatar_url("a/b", "photo name.jpg"),
            "/static/avatars/a%2Fb/photo%20name.jpg",
        )

    def test_records_avatar_history_with_postgresql_booleans(self):
        from server.avatar_history import record_user_avatar

        class Cursor:
            def __init__(self):
                self.executions = []

            def execute(self, query, params):
                self.executions.append((query, params))

        class Connection:
            def __init__(self):
                self.cursor_instance = Cursor()
                self.committed = False
                self.closed = False

            def cursor(self):
                return self.cursor_instance

            def commit(self):
                self.committed = True

            def close(self):
                self.closed = True

        connection = Connection()
        with patch("server.avatar_history.get_connection", return_value=connection), patch(
            "server.avatar_history.utc_now_iso", return_value="2026-09-27T00:00:00Z"
        ):
            record_user_avatar(7, "/static/avatars/user/avatar.jpg")

        self.assertIn("is_current = FALSE", connection.cursor_instance.executions[1][0])
        self.assertIn("VALUES (?, ?, ?, TRUE)", connection.cursor_instance.executions[2][0])
        self.assertTrue(connection.committed)
        self.assertTrue(connection.closed)

    def test_default_avatar_static_route_returns_ok(self):
        from starlette.testclient import TestClient

        from server.main import app

        response = TestClient(app).get("/static/avatars/default.jpg")

        self.assertEqual(response.status_code, 200)


class PrivacySettingsTests(unittest.TestCase):
    def test_updates_read_receipts_with_a_boolean_parameter(self):
        from server.routes.auth import PrivacySettingsUpdate, update_my_privacy

        class Cursor:
            def __init__(self):
                self.executions = []

            def execute(self, query, params):
                self.executions.append((query, params))

        class Connection:
            def __init__(self):
                self.cursor_instance = Cursor()

            def cursor(self):
                return self.cursor_instance

            def commit(self):
                pass

            def close(self):
                pass

        connection = Connection()
        with patch("server.routes.auth.get_connection", return_value=connection), patch(
            "server.routes.auth.get_privacy_settings", return_value={}
        ):
            asyncio.run(
                update_my_privacy(
                    PrivacySettingsUpdate(read_receipts_enabled=False),
                    {"id": 7},
                )
            )

        _, parameters = connection.cursor_instance.executions[0]
        self.assertIs(parameters[0], False)
        self.assertEqual(parameters[1], 7)


class MarkChatReadTests(unittest.TestCase):
    def test_mark_all_uses_rows_fetched_before_other_cursor_queries(self):
        from server.routes.chats import MarkChatReadRequest, mark_chat_read

        class Cursor:
            def __init__(self):
                self.rows = []
                self.updates = []

            def execute(self, query, params=None):
                if query.lstrip().startswith("UPDATE messages SET read_by"):
                    self.updates.append(params)
                self.rows = (
                    [{"id": 5, "sender_id": 2, "read_by": "[]", "undelivered_to": "[]"}]
                    if "FROM messages" in query
                    else []
                )

            def fetchall(self):
                return self.rows

        class Connection:
            def __init__(self):
                self.cursor_instance = Cursor()

            def cursor(self):
                return self.cursor_instance

            def commit(self):
                pass

            def rollback(self):
                pass

            def close(self):
                pass

        connection = Connection()

        def read_receipts_enabled(cursor, user_id):
            cursor.execute("SELECT read_receipts_enabled FROM privacy_settings WHERE user_id = ?", (user_id,))
            return True

        async def send_to_user(*args, **kwargs):
            pass

        async def broadcast_personalized(*args, **kwargs):
            pass

        with patch("server.routes.chats.get_connection", return_value=connection), patch(
            "server.routes.chats._ensure_chat_participant"
        ), patch("server.routes.chats.read_receipts_enabled", side_effect=read_receipts_enabled), patch(
            "server.routes.chats.serialize_user_snapshot", return_value={"id": 1, "user_id": 1}
        ), patch(
            "server.routes.chats.get_chat_unread_summary",
            return_value={"unread_count": 0, "first_unread_message_id": None},
        ), patch("server.routes.chats.manager.send_to_user", send_to_user), patch(
            "server.routes.chats.manager.broadcast_personalized", broadcast_personalized
        ):
            response = asyncio.run(
                mark_chat_read(3, MarkChatReadRequest(mark_all=True), {"id": 1})
            )

        self.assertEqual(response["read_message_ids"], [5])
        self.assertEqual(len(connection.cursor_instance.updates), 1)


class DeleteForMeSummaryTests(unittest.TestCase):
    def setUp(self):
        import sqlite3

        self.connection = sqlite3.connect(":memory:")
        self.connection.row_factory = sqlite3.Row
        self.cursor = self.connection.cursor()
        self.cursor.execute(
            """
            CREATE TABLE messages (
                id INTEGER PRIMARY KEY,
                chat_id INTEGER,
                sender_id INTEGER,
                sender_name TEXT,
                content TEXT,
                timestamp TEXT,
                edited_at TEXT,
                reactions TEXT,
                read_by TEXT,
                delivery_error TEXT,
                undelivered_to TEXT,
                deleted_for TEXT
            )
            """
        )

    def tearDown(self):
        self.connection.close()

    def add_message(self, message_id, sender_id=2, deleted_for=None, undelivered_to=None):
        import json

        self.cursor.execute(
            """
            INSERT INTO messages (id, chat_id, sender_id, sender_name, content, timestamp,
                                  reactions, read_by, undelivered_to, deleted_for)
            VALUES (?, 1, ?, 'user', ?, '2024-01-01T00:00:00', '[]', '[]', ?, ?)
            """,
            (
                message_id,
                sender_id,
                f"message {message_id}",
                json.dumps(undelivered_to or []),
                json.dumps(deleted_for or []),
            ),
        )

    def test_message_visible_to_hides_messages_deleted_for_user(self):
        from server.chat_summary import message_visible_to

        self.add_message(1, deleted_for=[7])
        self.add_message(2, sender_id=7, deleted_for=[7])
        self.add_message(3, deleted_for=[8])
        rows = {
            row["id"]: row
            for row in self.cursor.execute("SELECT * FROM messages").fetchall()
        }

        self.assertFalse(message_visible_to(rows[1], 7))
        self.assertFalse(message_visible_to(rows[2], 7))
        self.assertTrue(message_visible_to(rows[3], 7))
        self.assertTrue(message_visible_to(rows[1], 8))

    def test_message_visible_to_tolerates_rows_without_deleted_for(self):
        from server.chat_summary import message_visible_to

        self.assertTrue(message_visible_to({"sender_id": 2, "undelivered_to": "[]"}, 7))

    def test_last_message_skips_messages_deleted_for_user(self):
        from server.chat_summary import get_chat_unread_summary

        self.add_message(1)
        self.add_message(2)
        self.add_message(3, deleted_for=[7])

        summary = get_chat_unread_summary(self.cursor, 1, 7)
        other_summary = get_chat_unread_summary(self.cursor, 1, 8)

        self.assertEqual(summary["last_message"]["id"], 2)
        self.assertEqual(other_summary["last_message"]["id"], 3)

    def test_last_message_is_none_when_everything_is_deleted_for_user(self):
        from server.chat_summary import get_chat_unread_summary

        self.add_message(1, deleted_for=[7])
        self.add_message(2, deleted_for=[7])

        summary = get_chat_unread_summary(self.cursor, 1, 7)

        self.assertIsNone(summary["last_message"])
        self.assertEqual(summary["unread_count"], 0)

    def test_last_message_search_spans_multiple_batches(self):
        from server.chat_summary import get_chat_unread_summary

        self.add_message(1)
        for message_id in range(2, 130):
            self.add_message(message_id, deleted_for=[7])

        summary = get_chat_unread_summary(self.cursor, 1, 7)

        self.assertEqual(summary["last_message"]["id"], 1)

    def test_unread_count_excludes_messages_deleted_for_user(self):
        from server.chat_summary import get_chat_unread_summary

        self.add_message(1)
        self.add_message(2, deleted_for=[7])
        self.add_message(3)

        summary = get_chat_unread_summary(self.cursor, 1, 7)

        self.assertEqual(summary["unread_count"], 2)
        self.assertEqual(summary["first_unread_message_id"], 1)


class DeleteMessageForMeEndpointTests(unittest.TestCase):
    def test_notifies_only_the_requesting_user(self):
        import json
        import sqlite3

        from server.routes.messages import delete_message_for_me

        connection = sqlite3.connect(":memory:")
        connection.row_factory = sqlite3.Row
        cursor = connection.cursor()
        cursor.executescript(
            """
            CREATE TABLE chats (id INTEGER PRIMARY KEY, type TEXT);
            CREATE TABLE participants (chat_id INTEGER, user_id INTEGER);
            CREATE TABLE messages (
                id INTEGER PRIMARY KEY, chat_id INTEGER, sender_id INTEGER, sender_name TEXT,
                content TEXT, timestamp TEXT, edited_at TEXT, reactions TEXT, read_by TEXT,
                delivery_error TEXT, undelivered_to TEXT, deleted_for TEXT
            );
            INSERT INTO chats VALUES (1, 'group');
            INSERT INTO participants VALUES (1, 7), (1, 8);
            INSERT INTO messages VALUES
                (1, 1, 2, 'u', 'first', '2024-01-01T00:00:00', NULL, '[]', '[]', NULL, '[]', '[]'),
                (2, 1, 2, 'u', 'second', '2024-01-01T00:00:01', NULL, '[]', '[]', NULL, '[]', '[]');
            """
        )
        connection.commit()

        class Connection:
            def cursor(self):
                return cursor

            def commit(self):
                connection.commit()

            def rollback(self):
                connection.rollback()

            def close(self):
                pass

        broadcasts = []

        async def broadcast_personalized(chat_id, build_message):
            broadcasts.append((chat_id, {rid: build_message(rid) for rid in (7, 8)}))

        with patch("server.routes.messages.get_connection", return_value=Connection()), patch(
            "server.routes.messages.manager.broadcast_personalized", broadcast_personalized
        ):
            response = asyncio.run(delete_message_for_me(2, {"id": 7}))
            repeated = asyncio.run(delete_message_for_me(2, {"id": 7}))

        self.assertEqual(response, {"message": "Message deleted for you"})
        self.assertEqual(repeated, {"message": "Message deleted for you"})
        stored = cursor.execute("SELECT deleted_for FROM messages WHERE id = 2").fetchone()[0]
        self.assertEqual(json.loads(stored), [7])

        room_chat_id, room_payloads = broadcasts[0]
        self.assertEqual(room_chat_id, 1)
        self.assertIsNone(room_payloads[8])
        self.assertEqual(room_payloads[7]["type"], "delete")
        self.assertEqual(room_payloads[7]["chat_id"], 1)
        self.assertEqual(room_payloads[7]["message_id"], 2)

        list_chat_id, list_payloads = broadcasts[1]
        self.assertEqual(list_chat_id, 0)
        self.assertIsNone(list_payloads[8])
        self.assertEqual(list_payloads[7]["type"], "chat_list_delete")
        self.assertEqual(list_payloads[7]["message_id"], 2)
        self.assertEqual(list_payloads[7]["last_message"]["id"], 1)
        connection.close()

if __name__ == "__main__":
    unittest.main()

class VersionTests(unittest.TestCase):
    def test_version_is_semver_baseline(self):
        from server.version import __version__

        self.assertRegex(__version__, r"^\d+\.\d+\.\d+$")
