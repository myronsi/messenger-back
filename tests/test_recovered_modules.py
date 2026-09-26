import os
import tempfile
import unittest


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


if __name__ == "__main__":
    unittest.main()
