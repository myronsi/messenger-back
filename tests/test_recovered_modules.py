import asyncio
import unittest
from unittest.mock import patch


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
        from server.main import app

        static_route = next(route for route in app.routes if route.path == "/static")
        response = asyncio.run(
            static_route.app.get_response(
                "avatars/default.jpg",
                {"type": "http", "method": "GET", "headers": []},
            )
        )

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

if __name__ == "__main__":
    unittest.main()
