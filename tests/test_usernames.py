"""Usernames are validated, stored lowercase, unique case-insensitively and never used to build file paths."""
import io
import os
import shutil
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from fastapi import HTTPException

from server.usernames import escape_like, normalize_username
from tests.test_delete_for_me_postgres import TEST_DATABASE_URL, PostgresFixture, run
from tests.test_upload_security import make_image


class UsernameRuleTests(unittest.TestCase):
    def test_valid_names_are_lowercased(self):
        self.assertEqual(normalize_username("Alice_01"), "alice_01")
        self.assertEqual(normalize_username("a" * 32), "a" * 32)
        self.assertEqual(normalize_username("abc"), "abc")

    def test_unsafe_or_ambiguous_names_are_rejected(self):
        bad = [
            "../../something", "a/b", "a\\b", "..", "al ice", " alice", "alice ", "alice\n", "al\x00ice", "al\tice",
            "ab", "a" * 33, "", None, "ali-ce", "alice.", "al@ice",
            "\uff41lice",  # full-width a
            "\u0430lice",  # Cyrillic a
            "\u212alice",  # Kelvin sign lowercases to ASCII k
        ]
        for name in bad:
            with self.assertRaises(HTTPException, msg=repr(name)) as raised:
                normalize_username(name)
            self.assertEqual(raised.exception.status_code, 400)

    def test_escape_like(self):
        self.assertEqual(escape_like("a%b_c\\d"), "a\\%b\\_c\\\\d")


@unittest.skipUnless(TEST_DATABASE_URL, "TEST_DATABASE_URL is not set")
class UsernameDatabaseTests(PostgresFixture, unittest.TestCase):
    def setUp(self):
        super().setUp()
        from fastapi import FastAPI
        from starlette.testclient import TestClient

        from server.routes import auth

        patcher = patch.object(auth, "split_master_key", lambda key: ["s1", "s2", "s3"])
        patcher.start()
        self.addCleanup(patcher.stop)
        app = FastAPI()
        app.include_router(auth.router, prefix="/auth")
        self.client = TestClient(app)
        self.addCleanup(self.client.close)
        self.workdir = tempfile.mkdtemp()
        self.previous_cwd = os.getcwd()
        os.chdir(self.workdir)
        self.addCleanup(shutil.rmtree, self.workdir, True)
        self.addCleanup(os.chdir, self.previous_cwd)

    def register(self, username, password="password123"):
        return self.client.post(
            "/auth/register", json={"username": username, "display_name": "Some Name", "password": password}
        )

    def usernames(self):
        conn = self.database.get_connection()
        cursor = conn.cursor()
        cursor.execute("SELECT username FROM users WHERE id > 2 ORDER BY id")
        rows = [row["username"] for row in cursor.fetchall()]
        conn.close()
        return rows

    def test_registration_rejects_unsafe_usernames(self):
        for name in ("../../something", "a/b", "al ice", "al\nice", "ab", "x" * 33, "ali.ce"):
            response = self.register(name)
            self.assertEqual(response.status_code, 400, repr(name))
        self.assertEqual(self.usernames(), [])

    def test_registration_normalises_and_blocks_case_only_duplicates(self):
        self.assertEqual(self.register("Carol_1").status_code, 200)
        self.assertEqual(self.usernames(), ["carol_1"])
        for name in ("carol_1", "CAROL_1", "Carol_1"):
            response = self.register(name)
            self.assertEqual(response.status_code, 400)
            self.assertEqual(response.json()["detail"], "User already exists")
        self.assertEqual(self.usernames(), ["carol_1"])

    def test_login_ignores_case(self):
        self.register("Carol_1")
        for name in ("carol_1", "CAROL_1"):
            response = self.client.post("/auth/login", json={"username": name, "password": "password123"})
            self.assertEqual(response.status_code, 200, name)
        self.assertEqual(
            self.client.post("/auth/login", json={"username": "carol_1", "password": "wrong-password"}).status_code, 401
        )

    def test_database_enforces_case_insensitive_uniqueness(self):
        conn = self.database.get_connection()
        cursor = conn.cursor()
        cursor.execute("SELECT indexdef FROM pg_indexes WHERE indexname = 'idx_users_username_lower'")
        self.assertIn("lower(username)", cursor.fetchone()["indexdef"].lower())
        cursor.execute("INSERT INTO users (username, display_name, password) VALUES ('ALICE', 'Dup', 'x')")
        self.assertIsNone(cursor.lastrowid)
        conn.rollback()
        conn.close()

    def test_search_treats_wildcards_literally(self):
        from server.routes.users import search_users

        conn = self.database.get_connection()
        cursor = conn.cursor()
        for name in ("a_b", "axb", "100%sure", "100xsure"):
            self.add_user(cursor, name)
        conn.commit()
        conn.close()

        def search(q):
            result = run(search_users(q, current_user={"id": self.alice}))
            return sorted(user["username"] for user in result["users"])

        self.assertEqual(search("a_b"), ["a_b"])
        self.assertEqual(search("100%"), ["100%sure"])
        self.assertEqual(search("%"), ["100%sure"])
        self.assertEqual(search("_"), ["a_b"])
        self.assertEqual(search("A_B"), ["a_b"])

    def test_avatar_paths_never_use_the_username(self):
        from server.routes.auth import upload_avatar
        from starlette.datastructures import UploadFile

        conn = self.database.get_connection()
        cursor = conn.cursor()
        evil_id = self.add_user(cursor, "../../escaped")
        conn.commit()
        conn.close()
        user = {"id": evil_id, "username": "../../escaped", "display_name": "Evil"}
        result = run(upload_avatar(file=UploadFile(file=io.BytesIO(make_image("PNG")), filename="a.png"), current_user=user))

        root = Path(self.workdir)
        stored = [path.relative_to(root).as_posix() for path in root.rglob("*") if path.is_file()]
        self.assertEqual(len(stored), 1)
        self.assertTrue(stored[0].startswith(f"static/avatars/id-{evil_id}/"), stored)
        self.assertEqual(result["avatar_url"], f"/{stored[0]}")

    def test_store_user_avatar_rejects_escaping_ids(self):
        from server.avatar_history import store_user_avatar

        with self.assertRaises((ValueError, TypeError)):
            store_user_avatar("../../x", b"data", ".jpg")
        self.assertEqual([p for p in Path(self.workdir).rglob("*") if p.is_file()], [])

    def test_avatar_directory_is_authorised_through_the_owner(self):
        from server.media_access import can_access_media

        conn = self.database.get_connection()
        cursor = conn.cursor()
        cursor.execute("INSERT INTO user_privacy_settings (user_id, avatar_visibility) VALUES (?, 'nobody')", (self.alice,))
        conn.commit()
        self.assertTrue(can_access_media(cursor, self.alice, f"avatars/id-{self.alice}/x.jpg"))
        self.assertFalse(can_access_media(cursor, self.bob, f"avatars/id-{self.alice}/x.jpg"))
        self.assertFalse(can_access_media(cursor, self.bob, "avatars/id-99999/x.jpg"))
        # Directories created before this change are named after the username
        self.assertTrue(can_access_media(cursor, self.alice, "avatars/alice/x.jpg"))
        self.assertFalse(can_access_media(cursor, self.bob, "avatars/alice/x.jpg"))
        conn.close()


if __name__ == "__main__":
    unittest.main()
