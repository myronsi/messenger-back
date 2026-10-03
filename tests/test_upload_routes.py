"""Upload routes reject disguised active content and re-encode avatars.

Needs a disposable PostgreSQL database in TEST_DATABASE_URL (see test_delete_for_me_postgres).
"""
import io
import os
import shutil
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from fastapi import HTTPException, UploadFile

from tests.test_delete_for_me_postgres import TEST_DATABASE_URL, PostgresFixture, run
from tests.test_upload_security import make_image

HTML = b"<html><script>fetch('/auth/refresh', {method: 'POST'})</script></html>"


@unittest.skipUnless(TEST_DATABASE_URL, "TEST_DATABASE_URL is not set")
class UploadRouteTests(PostgresFixture, unittest.TestCase):
    def setUp(self):
        super().setUp()
        self.workdir = tempfile.mkdtemp()
        self.previous_cwd = os.getcwd()
        os.chdir(self.workdir)
        self.addCleanup(shutil.rmtree, self.workdir, True)
        self.addCleanup(os.chdir, self.previous_cwd)
        self.user = {"id": self.alice, "username": "alice", "display_name": "Alice"}

        async def noop(*args, **kwargs):
            return None

        for target in ("server.routes.messages.manager.broadcast_personalized", "server.routes.messages.send_chat_list_message"):
            patcher = patch(target, noop)
            patcher.start()
            self.addCleanup(patcher.stop)

    @staticmethod
    def upload(name, body):
        return UploadFile(file=io.BytesIO(body), filename=name)

    def send(self, name, body):
        from server.routes.messages import upload_file

        return run(upload_file(chat_id=self.chat_id, caption=None, file=self.upload(name, body), current_user=self.user))

    def test_media_extensions_require_matching_content(self):
        for name in ("evil.png", "evil.jpg", "evil.gif", "evil.pdf", "evil.mp4", "evil.mp3"):
            with self.assertRaises(HTTPException, msg=name) as raised:
                self.send(name, HTML)
            self.assertEqual(raised.exception.status_code, 400)
        self.assertFalse(Path("static/uploads").exists())

    def test_svg_is_not_accepted(self):
        with self.assertRaises(HTTPException) as raised:
            self.send("logo.svg", b"<svg xmlns='http://www.w3.org/2000/svg'><script>1</script></svg>")
        self.assertEqual(raised.exception.status_code, 400)

    def test_genuine_image_is_accepted_and_filename_is_sanitised(self):
        result = self.send("../../photo.png", make_image("PNG"))
        saved = list(Path("static/uploads").iterdir())
        self.assertEqual(len(saved), 1)
        self.assertTrue(saved[0].name.endswith("_photo.png"))
        self.assertTrue(result["file_url"].startswith("/static/uploads/"))

    def test_image_upload_records_dimensions_and_a_thumbnail_bound_to_the_message(self):
        import json

        from server.database import get_connection

        result = self.send("big.jpg", make_image("JPEG", size=(2000, 1000)))
        conn = get_connection()
        try:
            cursor = conn.cursor()
            cursor.execute("SELECT id, content FROM messages WHERE chat_id = ?", (self.chat_id,))
            message = cursor.fetchone()
            content = json.loads(message["content"])
            self.assertEqual((content["image_width"], content["image_height"]), (2000, 1000))
            self.assertTrue(content["thumbnail_url"].startswith("/static/uploads/"))
            self.assertNotEqual(content["thumbnail_url"], result["file_url"])
            self.assertTrue((Path("static") / content["thumbnail_url"].removeprefix("/static/")).is_file())
            cursor.execute("SELECT file_path FROM message_attachments WHERE message_id = ?", (message["id"],))
            recorded = {row["file_path"] for row in cursor.fetchall()}
            self.assertEqual(recorded, {result["file_url"].removeprefix("/static/"), content["thumbnail_url"].removeprefix("/static/")})
        finally:
            conn.close()

    def test_small_image_upload_has_dimensions_but_no_thumbnail(self):
        import json

        from server.database import get_connection

        self.send("small.png", make_image("PNG", size=(40, 20)))
        conn = get_connection()
        try:
            cursor = conn.cursor()
            cursor.execute("SELECT content FROM messages WHERE chat_id = ?", (self.chat_id,))
            content = json.loads(cursor.fetchone()["content"])
        finally:
            conn.close()
        self.assertEqual((content["image_width"], content["image_height"]), (40, 20))
        self.assertNotIn("thumbnail_url", content)
        self.assertEqual(len(list(Path("static/uploads").iterdir())), 1)

    def avatar(self, route, name, body, **kwargs):
        return run(route(file=self.upload(name, body), **kwargs))

    def test_avatar_is_reencoded_and_never_keeps_user_supplied_name_or_type(self):
        from server.routes.auth import upload_avatar

        result = self.avatar(upload_avatar, "<svg onload=x>.svg", make_image("PNG"), current_user=self.user)
        saved = next(Path(f"static/avatars/id-{self.alice}").iterdir())
        self.assertEqual(saved.suffix, ".jpg")
        self.assertTrue(saved.read_bytes().startswith(b"\xff\xd8\xff"))
        self.assertTrue(result["avatar_url"].endswith(saved.name))

    def test_avatar_rejects_html_svg_and_oversized_files(self):
        from server.routes.auth import upload_avatar

        for name, body in (
            ("a.html", HTML),
            ("a.svg", b"<svg xmlns='http://www.w3.org/2000/svg'><script>1</script></svg>"),
            ("a.png", HTML),
            ("big.png", make_image("PNG") + b"0" * (5 * 1024 * 1024)),
        ):
            with self.assertRaises(HTTPException, msg=name) as raised:
                self.avatar(upload_avatar, name, body, current_user=self.user)
            self.assertEqual(raised.exception.status_code, 400)
        self.assertFalse(Path(f"static/avatars/id-{self.alice}").exists())

    def test_group_avatar_is_validated_and_reencoded(self):
        from server.routes import groups

        async def noop(*args, **kwargs):
            return {}

        with patch.object(groups, "_ensure_role"), patch.object(groups, "_broadcast_group_update", noop), \
                patch.object(groups, "_get_group_details", lambda *args: {}):
            with self.assertRaises(HTTPException):
                self.avatar(groups.upload_group_avatar, "g.svg", HTML, chat_id=self.chat_id, current_user=self.user)
            self.avatar(groups.upload_group_avatar, "g.png", make_image("PNG"), chat_id=self.chat_id, current_user=self.user)
        saved = list(Path("static/avatars/groups").iterdir())
        self.assertEqual(len(saved), 1)
        self.assertEqual(saved[0].suffix, ".jpg")

    def test_duplicate_avatar_route_is_gone(self):
        from server.routes import users

        self.assertFalse(hasattr(users, "upload_avatar"))
        paths = {route.path for route in users.router.routes}
        self.assertNotIn("/users/me/avatar", paths)


if __name__ == "__main__":
    unittest.main()
