"""Session cookies must be scoped to the public URL path, which differs behind a reverse proxy (MSGC-40)."""
import os
import unittest
from datetime import datetime, timedelta, timezone
from unittest.mock import patch

from fastapi import Response

from server.routes import auth


def cookie_headers(response):
    return [value.decode() for key, value in response.raw_headers if key == b"set-cookie"]


class CookieScopeTests(unittest.TestCase):
    def issue(self):
        response = Response()
        expires_at = datetime.now(timezone.utc) + timedelta(days=1)
        auth.set_refresh_cookie(response, "refresh", expires_at)
        auth.set_media_cookie(response, 1, "session", expires_at)
        return cookie_headers(response)

    def test_default_paths_without_a_proxy_prefix(self):
        with patch.dict(os.environ, {}, clear=False):
            os.environ.pop("COOKIE_PATH_PREFIX", None)
            os.environ.pop("COOKIE_SECURE", None)
            refresh, media = self.issue()
        self.assertIn("Path=/auth", refresh)
        self.assertIn("Path=/static", media)
        self.assertNotIn("Secure", refresh + media)

    def test_cookies_follow_the_proxy_prefix_and_secure_flag(self):
        with patch.dict(os.environ, {"COOKIE_PATH_PREFIX": "/api/", "COOKIE_SECURE": "true"}):
            refresh, media = self.issue()
            cleared = Response()
            auth.clear_refresh_cookie(cleared)
            cleared_headers = cookie_headers(cleared)
        self.assertIn("Path=/api/auth", refresh)
        self.assertIn("Path=/api/static", media)
        self.assertIn("Secure", refresh)
        self.assertIn("Secure", media)
        self.assertTrue(any("Path=/api/auth" in header for header in cleared_headers))
        self.assertTrue(any("Path=/api/static" in header for header in cleared_headers))

    def test_prefix_without_leading_slash_is_normalised(self):
        with patch.dict(os.environ, {"COOKIE_PATH_PREFIX": "api"}):
            self.assertEqual(auth.cookie_path("/static"), "/api/static")


if __name__ == "__main__":
    unittest.main()