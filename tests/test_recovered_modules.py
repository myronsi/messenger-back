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


if __name__ == "__main__":
    unittest.main()
