"""Files under /static are only served to users allowed to see them (MSGC-40).

Needs a disposable PostgreSQL database in TEST_DATABASE_URL (see test_delete_for_me_postgres).
"""
import json
import shutil
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path
from unittest.mock import patch

from tests.test_delete_for_me_postgres import TEST_DATABASE_URL, PostgresFixture

UPLOAD_URL = "/static/uploads/aaaa_photo.png"
VOICE_URL = "/static/vm/bbbb.webm"


@unittest.skipUnless(TEST_DATABASE_URL, "TEST_DATABASE_URL is not set")
class MediaAccessTests(PostgresFixture, unittest.TestCase):
    """alice and bob share a chat; carol is outside it."""

    def setUp(self):
        super().setUp()
        from fastapi import FastAPI
        from starlette.testclient import TestClient

        from server import tokens
        from server.routes import media

        self.tokens = tokens
        self.static = Path(tempfile.mkdtemp())
        for rel_path, body in {
            "uploads/aaaa_photo.png": b"photo-bytes",
            "uploads/cccc_page.html": b"<script>1</script>",
            "uploads/dddd_disguised.png": b"<html><script>1</script></html>",
            "uploads/eeee_real.png": b"\x89PNG\r\n\x1a\n" + b"0" * 24,
            "vm/bbbb.webm": b"voice-bytes",
            "avatars/default.jpg": b"default-avatar",
            "avatars/alice/me.png": b"alice-avatar",
            "avatars/groups/room.png": b"group-avatar",
        }.items():
            target = self.static / rel_path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(body)
        self.addCleanup(shutil.rmtree, self.static, True)
        patcher = patch.object(media, "STATIC_ROOT", self.static)
        patcher.start()
        self.addCleanup(patcher.stop)

        conn = self.database.get_connection()
        cursor = conn.cursor()
        self.carol = self.add_user(cursor, "carol")
        cursor.execute("UPDATE users SET avatar_url = '/static/avatars/alice/me.png' WHERE id = ?", (self.alice,))
        cursor.execute("UPDATE chats SET avatar_url = '/static/avatars/groups/room.png' WHERE id = ?", (self.chat_id,))
        conn.commit()
        conn.close()

        app = FastAPI()
        app.include_router(media.router)
        self.client = TestClient(app)
        self.addCleanup(self.client.close)

    def login(self, user_id):
        """Create a session and return (bearer headers, media cookie value)."""
        conn = self.database.get_connection()
        cursor = conn.cursor()
        session_id = f"session-{user_id}"
        now = datetime.now(timezone.utc)
        cursor.execute(
            """
            INSERT INTO user_sessions (id, user_id, refresh_token_hash, created_at, last_active_at, expires_at)
            VALUES (?, ?, 'x', ?, ?, ?)
            """,
            (session_id, user_id, now.isoformat(), now.isoformat(), (now + timedelta(days=1)).isoformat()),
        )
        conn.commit()
        conn.close()
        access = self.tokens.create_token(self.tokens.TOKEN_ACCESS, user_id, timedelta(minutes=5), sid=session_id)
        media = self.tokens.create_token(self.tokens.TOKEN_MEDIA, user_id, timedelta(minutes=5), sid=session_id)
        return {"Authorization": f"Bearer {access}"}, media, session_id

    def send_file(self, sender_id, file_url):
        from server.media_access import record_attachment

        message_id = self.add_message("file", sender_id=sender_id, content=json.dumps({"file_url": file_url}))
        conn = self.database.get_connection()
        record_attachment(conn.cursor(), message_id, file_url)
        conn.commit()
        conn.close()
        return message_id

    def get(self, path, user_id=None, cookie=False):
        self.client.cookies.clear()
        headers = {}
        if user_id is not None:
            bearer, media_cookie, _ = self.login(user_id)
            if cookie:
                self.client.cookies.set("media_session", media_cookie)
            else:
                headers = bearer
        return self.client.get(path, headers=headers)
    def test_chat_member_gets_the_file_with_bearer_or_cookie(self):
        self.send_file(self.bob, UPLOAD_URL)
        for cookie in (False, True):
            response = self.get(UPLOAD_URL, self.alice, cookie=cookie)
            self.assertEqual(response.status_code, 200)
            self.assertEqual(response.content, b"photo-bytes")
            self.assertEqual(response.headers["x-content-type-options"], "nosniff")

    def test_voice_messages_are_protected_too(self):
        self.send_file(self.bob, VOICE_URL)
        self.assertEqual(self.get(VOICE_URL, self.alice).content, b"voice-bytes")
        self.assertEqual(self.get(VOICE_URL, self.carol).status_code, 404)

    def test_anonymous_request_is_rejected(self):
        self.send_file(self.bob, UPLOAD_URL)
        self.assertEqual(self.get(UPLOAD_URL).status_code, 401)
        self.client.cookies.set("media_session", "garbage")
        self.assertEqual(self.client.get(UPLOAD_URL).status_code, 401)

    def test_non_member_cannot_read_attachments(self):
        self.send_file(self.bob, UPLOAD_URL)
        for cookie in (False, True):
            self.assertEqual(self.get(UPLOAD_URL, self.carol, cookie=cookie).status_code, 404)

    def test_unreferenced_file_is_not_served(self):
        self.assertEqual(self.get(UPLOAD_URL, self.alice).status_code, 404)

    def test_forged_message_content_does_not_grant_access(self):
        self.send_file(self.bob, UPLOAD_URL)
        conn = self.database.get_connection()
        cursor = conn.cursor()
        cursor.execute("INSERT INTO chats (name, type) VALUES ('mallory room', 'group')")
        other_chat = cursor.lastrowid
        cursor.execute("INSERT INTO participants (chat_id, user_id) VALUES (?, ?)", (other_chat, self.carol))
        cursor.execute(
            "INSERT INTO messages (chat_id, sender_id, sender_name, content) VALUES (?, ?, 'carol', ?)",
            (other_chat, self.carol, json.dumps({"file_url": UPLOAD_URL})),
        )
        conn.commit()
        conn.close()
        self.assertEqual(self.get(UPLOAD_URL, self.carol).status_code, 404)

    def test_message_deleted_for_the_user_hides_the_file_from_them_only(self):
        message_id = self.send_file(self.bob, UPLOAD_URL)
        conn = self.database.get_connection()
        conn.cursor().execute("UPDATE messages SET deleted_for = ? WHERE id = ?", (json.dumps([self.alice]), message_id))
        conn.commit()
        conn.close()
        self.assertEqual(self.get(UPLOAD_URL, self.alice).status_code, 404)
        self.assertEqual(self.get(UPLOAD_URL, self.bob).status_code, 200)

    def test_recipient_with_undelivered_message_cannot_read_it(self):
        message_id = self.send_file(self.bob, UPLOAD_URL)
        conn = self.database.get_connection()
        conn.cursor().execute("UPDATE messages SET undelivered_to = ? WHERE id = ?", (json.dumps([self.alice]), message_id))
        conn.commit()
        conn.close()
        self.assertEqual(self.get(UPLOAD_URL, self.alice).status_code, 404)
        self.assertEqual(self.get(UPLOAD_URL, self.bob).status_code, 200)

    def test_hard_deleted_message_and_left_chat_revoke_access(self):
        message_id = self.send_file(self.bob, UPLOAD_URL)
        conn = self.database.get_connection()
        conn.cursor().execute("DELETE FROM participants WHERE chat_id = ? AND user_id = ?", (self.chat_id, self.alice))
        conn.commit()
        conn.close()
        self.assertEqual(self.get(UPLOAD_URL, self.alice).status_code, 404)

        conn = self.database.get_connection()
        conn.cursor().execute("DELETE FROM messages WHERE id = ?", (message_id,))
        conn.commit()
        conn.close()
        self.assertEqual(self.get(UPLOAD_URL, self.bob).status_code, 404)

    def test_revoked_session_is_rejected(self):
        self.send_file(self.bob, UPLOAD_URL)
        self.assertEqual(self.get(UPLOAD_URL, self.alice, cookie=True).status_code, 200)
        conn = self.database.get_connection()
        conn.cursor().execute("UPDATE user_sessions SET revoked_at = ? WHERE user_id = ?", (datetime.now(timezone.utc).isoformat(), self.alice))
        conn.commit()
        conn.close()
        self.assertEqual(self.get(UPLOAD_URL, self.alice, cookie=True).status_code, 401)

    def test_path_traversal_is_not_served(self):
        self.send_file(self.bob, UPLOAD_URL)
        for path in ("/static/%2e%2e/secret", "/static/uploads/%2e%2e/avatars/alice/me.png", "/static/uploads/..%2f..%2fsecret", "/static//etc/passwd"):
            self.assertIn(self.get(path, self.carol).status_code, (401, 404), path)

    def test_path_normalisation_rejects_escapes(self):
        from server.media_access import normalize_media_path

        for path in ("../x", "uploads/../x", "/etc/passwd", "uploads\\x", "uploads/x\0", ""):
            self.assertIsNone(normalize_media_path(path), path)
        self.assertEqual(normalize_media_path("uploads/a.png"), "uploads/a.png")

    def test_non_media_files_are_sent_as_downloads(self):
        self.send_file(self.bob, "/static/uploads/cccc_page.html")
        response = self.get("/static/uploads/cccc_page.html", self.alice)
        self.assertEqual(response.status_code, 200)
        self.assertTrue(response.headers["content-disposition"].startswith("attachment"))

    def test_html_disguised_as_an_image_is_never_rendered(self):
        self.send_file(self.bob, "/static/uploads/dddd_disguised.png")
        response = self.get("/static/uploads/dddd_disguised.png", self.alice)
        self.assertEqual(response.status_code, 200)
        self.assertTrue(response.headers["content-disposition"].startswith("attachment"))
        self.assertEqual(response.headers["content-type"], "application/octet-stream")
        self.assertEqual(response.headers["x-content-type-options"], "nosniff")
        self.assertIn("sandbox", response.headers["content-security-policy"])

    def test_genuine_images_stay_inline(self):
        self.send_file(self.bob, "/static/uploads/eeee_real.png")
        response = self.get("/static/uploads/eeee_real.png", self.alice)
        self.assertNotIn("attachment", response.headers.get("content-disposition", ""))
        self.assertEqual(response.headers["content-type"], "image/png")

    def test_default_avatar_is_public(self):
        response = self.get("/static/avatars/default.jpg")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.content, b"default-avatar")

    def test_avatar_respects_privacy_settings(self):
        path = "/static/avatars/alice/me.png"
        self.assertEqual(self.get(path).status_code, 401)
        self.assertEqual(self.get(path, self.bob).status_code, 200)

        conn = self.database.get_connection()
        cursor = conn.cursor()
        cursor.execute("INSERT INTO user_privacy_settings (user_id) VALUES (?)", (self.alice,))
        cursor.execute("UPDATE user_privacy_settings SET avatar_visibility = 'nobody' WHERE user_id = ?", (self.alice,))
        conn.commit()
        conn.close()
        self.assertEqual(self.get(path, self.bob).status_code, 404)
        self.assertEqual(self.get(path, self.alice).status_code, 200)

    def test_group_avatar_is_visible_to_members_only(self):
        path = "/static/avatars/groups/room.png"
        self.assertEqual(self.get(path, self.alice).status_code, 200)
        self.assertEqual(self.get(path, self.carol).status_code, 404)

    def test_forwarding_copies_attachment_rights(self):
        from server.media_access import copy_attachments

        source = self.send_file(self.bob, UPLOAD_URL)
        conn = self.database.get_connection()
        cursor = conn.cursor()
        cursor.execute("INSERT INTO chats (name, type) VALUES ('second', 'group')")
        second_chat = cursor.lastrowid
        cursor.execute("INSERT INTO participants (chat_id, user_id) VALUES (?, ?)", (second_chat, self.carol))
        cursor.execute(
            "INSERT INTO messages (chat_id, sender_id, sender_name, content) VALUES (?, ?, 'bob', ?)",
            (second_chat, self.bob, json.dumps({"file_url": UPLOAD_URL})),
        )
        forwarded = cursor.lastrowid
        copy_attachments(cursor, source, forwarded)
        conn.commit()
        conn.close()
        self.assertEqual(self.get(UPLOAD_URL, self.carol).status_code, 200)

    def test_backfill_recovers_attachments_of_existing_messages(self):
        from server.media_access import backfill_message_attachments

        self.add_message("file", sender_id=self.bob, content=json.dumps({"file_url": UPLOAD_URL}))
        self.add_message("text", sender_id=self.bob, content=json.dumps({"file_url": "https://example.com/x.png"}))
        self.add_message("hello", sender_id=self.bob)
        conn = self.database.get_connection()
        cursor = conn.cursor()
        backfill_message_attachments(cursor)
        backfill_message_attachments(cursor)
        cursor.execute("SELECT file_path FROM message_attachments")
        self.assertEqual([row["file_path"] for row in cursor.fetchall()], ["uploads/aaaa_photo.png"])
        conn.commit()
        conn.close()
        self.assertEqual(self.get(UPLOAD_URL, self.alice).status_code, 200)


if __name__ == "__main__":
    unittest.main()
