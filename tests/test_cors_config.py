"""CORS and host checks come from the environment; credentialed wildcards are never allowed."""
import os
import unittest
from unittest.mock import patch

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware
from fastapi.middleware.trustedhost import TrustedHostMiddleware
from starlette.testclient import TestClient

from server.config import DEFAULT_CORS_ORIGINS, allowed_hosts, cors_origins


class ConfigTests(unittest.TestCase):
    def test_defaults_are_local_dev_origins_only(self):
        self.assertEqual(cors_origins({}), list(DEFAULT_CORS_ORIGINS))
        self.assertEqual(cors_origins({"CORS_ORIGINS": "  "}), list(DEFAULT_CORS_ORIGINS))
        self.assertTrue(all(origin.startswith("http://") for origin in DEFAULT_CORS_ORIGINS))

    def test_origins_are_parsed_and_wildcard_dropped(self):
        env = {"CORS_ORIGINS": "https://a.example.com/, https://b.example.com ,*"}
        self.assertEqual(cors_origins(env), ["https://a.example.com", "https://b.example.com"])
        self.assertEqual(cors_origins({"CORS_ORIGINS": "*"}), [])

    def test_hosts_unset_disables_check(self):
        self.assertIsNone(allowed_hosts({}))
        self.assertEqual(allowed_hosts({"ALLOWED_HOSTS": "a.com, 127.0.0.1"}), ["a.com", "127.0.0.1"])


class MiddlewareTests(unittest.TestCase):
    def build(self, **env):
        with patch.dict(os.environ, env, clear=False):
            origins, hosts = cors_origins(), allowed_hosts()
        app = FastAPI()
        if hosts:
            app.add_middleware(TrustedHostMiddleware, allowed_hosts=hosts)
        app.add_middleware(CORSMiddleware, allow_origins=origins, allow_credentials=True,
                           allow_methods=["GET"], allow_headers=["Authorization"])

        @app.get("/")
        def root():
            return {"ok": True}

        return TestClient(app)

    def test_unlisted_origin_gets_no_cors_headers(self):
        client = self.build(CORS_ORIGINS="https://app.example.com")
        allowed = client.get("/", headers={"Origin": "https://app.example.com"})
        self.assertEqual(allowed.headers.get("access-control-allow-origin"), "https://app.example.com")
        denied = client.get("/", headers={"Origin": "https://evil.example.net"})
        self.assertNotIn("access-control-allow-origin", denied.headers)

    def test_host_header_is_checked_when_configured(self):
        client = self.build(ALLOWED_HOSTS="chat.example.com")
        self.assertEqual(client.get("/", headers={"Host": "chat.example.com"}).status_code, 200)
        self.assertEqual(client.get("/", headers={"Host": "evil.example.net"}).status_code, 400)


if __name__ == "__main__":
    unittest.main()
