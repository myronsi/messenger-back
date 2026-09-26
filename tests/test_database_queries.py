import unittest

from server.database import translate_query


class DatabaseQueryTests(unittest.TestCase):
    def test_translates_sqlite_placeholders_and_insert_ignore(self):
        self.assertEqual(
            translate_query("INSERT OR IGNORE INTO participants (chat_id, user_id) VALUES (?, ?)"),
            "INSERT INTO participants (chat_id, user_id) VALUES (%s, %s) ON CONFLICT DO NOTHING",
        )

    def test_preserves_postgresql_upserts(self):
        query = "INSERT INTO users (username) VALUES (?) ON CONFLICT(username) DO UPDATE SET username = excluded.username"
        self.assertEqual(
            translate_query(query),
            "INSERT INTO users (username) VALUES (%s) ON CONFLICT(username) DO UPDATE SET username = excluded.username",
        )

    def test_translates_qualified_case_insensitive_ordering(self):
        self.assertEqual(
            translate_query("SELECT u.username FROM users u ORDER BY u.username COLLATE NOCASE"),
            "SELECT u.username FROM users u ORDER BY LOWER(u.username)",
        )


if __name__ == "__main__":
    unittest.main()
