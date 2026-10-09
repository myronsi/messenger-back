"""Recovery cannot be driven without a user-held secret, is throttled and gives no hints about unknown users."""
import os
import shutil
import tempfile
import unittest
from unittest.mock import patch

from cryptography.fernet import Fernet
from fastapi import FastAPI, HTTPException
from starlette.requests import Request

from server import rate_limit
from server.recovery_shares import ENCRYPTED_PREFIX, decrypt_cloud_part, encrypt_cloud_part
from tests.test_delete_for_me_postgres import TEST_DATABASE_URL, PostgresFixture

SHARES = ["2-1-aa11", "2-2-bb22", "2-3-cc33"]


class FakeClock:
    def __init__(self):
        self.now = 1000.0

    def __call__(self):
        return self.now


def make_request(peer, headers=None):
    scope = {
        "type": "http",
        "client": (peer, 1234),
        "headers": [(k.lower().encode(), v.encode()) for k, v in (headers or {}).items()],
    }
    return Request(scope)


class LimiterTests(unittest.TestCase):
    def test_blocks_after_limit_and_expires(self):
        clock = FakeClock()
        limiter = rate_limit.SlidingWindowLimiter(limit=3, window_seconds=60, clock=clock)
        for _ in range(3):
            self.assertEqual(limiter.retry_after("k"), 0)
            limiter.hit("k")
        self.assertGreater(limiter.retry_after("k"), 0)
        self.assertEqual(limiter.retry_after("other"), 0)
        clock.now += 61
        self.assertEqual(limiter.retry_after("k"), 0)

    def test_enforce_raises_429_with_retry_after(self):
        limiter = rate_limit.SlidingWindowLimiter(limit=1, window_seconds=60)
        limiter.hit("k")
        with self.assertRaises(HTTPException) as raised:
            rate_limit.enforce(limiter, "k")
        self.assertEqual(raised.exception.status_code, 429)
        self.assertIn("Retry-After", raised.exception.headers)

    def test_reset_clears_key(self):
        limiter = rate_limit.SlidingWindowLimiter(limit=1, window_seconds=60)
        limiter.hit("k")
        limiter.reset("k")
        self.assertEqual(limiter.retry_after("k"), 0)

    def test_key_table_is_bounded(self):
        limiter = rate_limit.SlidingWindowLimiter(limit=1, window_seconds=60)
        limiter.MAX_KEYS = 5
        for index in range(50):
            limiter.hit(str(index))
        self.assertLessEqual(len(limiter._hits), 5)

    def test_client_ip_only_trusts_headers_from_private_peers(self):
        proxied = make_request("172.18.0.1", {"X-Real-IP": "203.0.113.5"})
        self.assertEqual(rate_limit.client_ip(proxied), "203.0.113.5")
        forwarded = make_request("127.0.0.1", {"X-Forwarded-For": "1.1.1.1, 203.0.113.9"})
        self.assertEqual(rate_limit.client_ip(forwarded), "203.0.113.9")
        direct = make_request("8.8.8.8", {"X-Real-IP": "1.2.3.4"})
        self.assertEqual(rate_limit.client_ip(direct), "8.8.8.8")


class CloudPartEncryptionTests(unittest.TestCase):
    def test_round_trip_and_legacy_values(self):
        stored = encrypt_cloud_part("2-2-bb22")
        self.assertTrue(stored.startswith(ENCRYPTED_PREFIX))
        self.assertNotIn("bb22", stored)
        self.assertEqual(decrypt_cloud_part(stored), "2-2-bb22")
        self.assertEqual(decrypt_cloud_part("2-2-bb22"), "2-2-bb22")
        self.assertIsNone(decrypt_cloud_part(None))

    def test_wrong_key_does_not_decrypt(self):
        stored = encrypt_cloud_part("2-2-bb22")
        self.assertIsNone(decrypt_cloud_part(stored, Fernet(Fernet.generate_key())))


@unittest.skipUnless(TEST_DATABASE_URL, "TEST_DATABASE_URL is not set")
class RecoveryRouteTests(PostgresFixture, unittest.TestCase):
    def setUp(self):
        super().setUp()
        from starlette.testclient import TestClient

        from server.routes import auth

        self.auth = auth
        self.master_keys = []

        def fake_split(key_hex):
            self.master_keys.append(key_hex)
            return list(SHARES)

        def fake_combine(shares):
            if len(set(shares)) == 2 and set(shares) <= set(SHARES):
                return self.master_keys[-1]
            raise Exception("bad shares")

        for name, fake in (("split_master_key", fake_split), ("combine_master_key", fake_combine)):
            patcher = patch.object(auth, name, fake)
            patcher.start()
            self.addCleanup(patcher.stop)
        rate_limit.clear_all()
        self.addCleanup(rate_limit.clear_all)
        app = FastAPI()
        app.include_router(auth.router, prefix="/auth")
        self.client = TestClient(app)
        self.addCleanup(self.client.close)
        self.workdir = tempfile.mkdtemp()
        self.previous_cwd = os.getcwd()
        os.chdir(self.workdir)
        self.addCleanup(shutil.rmtree, self.workdir, True)
        self.addCleanup(os.chdir, self.previous_cwd)
        response = self.client.post(
            "/auth/register", json={"username": "dave", "display_name": "Dave", "password": "password123"}
        )
        self.assertEqual(response.status_code, 200, response.text)

    def recover(self, username="dave", **parts):
        return self.client.post("/auth/recover", json={"username": username, **parts})

    def stored_cloud_part(self):
        conn = self.database.get_connection()
        cursor = conn.cursor()
        cursor.execute("SELECT encrypted_cloud_part FROM users WHERE username = 'dave'")
        value = cursor.fetchone()[0]
        conn.close()
        return value

    def test_cloud_part_endpoint_is_gone_and_share_is_encrypted(self):
        self.assertEqual(self.client.get("/auth/get-cloud-part", params={"username": "dave"}).status_code, 404)
        stored = self.stored_cloud_part()
        self.assertTrue(stored.startswith(ENCRYPTED_PREFIX))
        self.assertNotIn(SHARES[1], stored)

    def test_one_user_share_plus_server_share_recovers(self):
        for share in (SHARES[0], SHARES[2]):
            response = self.recover(part1=share)
            self.assertEqual(response.status_code, 200, response.text)
            self.assertIn("recovery_token", response.json())

    def test_two_user_shares_recover_without_server_share(self):
        self.assertEqual(self.recover(part1=SHARES[0], part2=SHARES[2]).status_code, 200)

    def test_server_share_alone_is_not_enough(self):
        self.assertEqual(self.recover(part1=SHARES[1]).status_code, 400)
        self.assertEqual(self.recover(part1="2-9-ffff").status_code, 400)

    def test_legacy_plaintext_cloud_part_still_works_and_is_migrated(self):
        conn = self.database.get_connection()
        cursor = conn.cursor()
        cursor.execute("UPDATE users SET encrypted_cloud_part = ? WHERE username = 'dave'", (SHARES[1],))
        conn.commit()
        conn.close()
        self.assertEqual(self.recover(part1=SHARES[0]).status_code, 200)
        self.database.setup_database()
        self.assertTrue(self.stored_cloud_part().startswith(ENCRYPTED_PREFIX))
        self.assertEqual(self.recover(part1=SHARES[0]).status_code, 200)

    def test_unknown_and_wrong_user_answers_are_identical(self):
        unknown = self.recover(username="nobody", part1=SHARES[0])
        wrong = self.recover(part1="2-9-ffff")
        malformed = self.recover(part1="x\n2-2-bb22")
        for response in (unknown, wrong, malformed):
            self.assertEqual(response.status_code, 400)
            self.assertEqual(response.json(), unknown.json())

    def test_recovery_is_throttled_per_username(self):
        codes = [self.recover(part1="2-9-ffff").status_code for _ in range(6)]
        self.assertEqual(codes[:5], [400] * 5)
        self.assertEqual(codes[5], 429)
        blocked = self.recover(part1=SHARES[0])
        self.assertEqual(blocked.status_code, 429)
        self.assertIn("retry-after", blocked.headers)

    def test_recovery_is_throttled_per_ip(self):
        statuses = [self.recover(username=f"user{index}", part1="2-9-ffff").status_code for index in range(11)]
        self.assertEqual(statuses[-1], 429)

    def test_login_is_throttled_and_success_resets_counter(self):
        def login(password, username="dave"):
            return self.client.post("/auth/login", json={"username": username, "password": password})

        for _ in range(7):
            self.assertEqual(login("wrong-password").status_code, 401)
        self.assertEqual(login("password123").status_code, 200)
        for _ in range(8):
            self.assertEqual(login("wrong-password").status_code, 401)
        self.assertEqual(login("password123").status_code, 429)
        self.assertEqual(login("x" * 10, username="other_user").status_code, 401)

    def test_reset_password_is_throttled(self):
        statuses = []
        for _ in range(21):
            response = self.client.post(
                "/auth/reset-password", json={"recovery_token": "bogus", "new_password": "password789"}
            )
            statuses.append(response.status_code)
        self.assertEqual(statuses[0], 401)
        self.assertEqual(statuses[-1], 429)

    def test_two_factor_attempts_are_throttled_per_ip(self):
        statuses = []
        for _ in range(21):
            response = self.client.post("/auth/login/2fa", json={"login_challenge": "bogus", "code": "000000"})
            statuses.append(response.status_code)
        self.assertEqual(statuses[0], 401)
        self.assertEqual(statuses[-1], 429)

    def test_reset_password_hashes_off_the_event_loop(self):
        import asyncio

        hash_password = self.auth.hash_password_with_salt
        on_loop = []

        def recording_hash(password):
            try:
                asyncio.get_running_loop()
                on_loop.append(True)
            except RuntimeError:
                on_loop.append(False)
            return hash_password(password)

        token = self.recover(part1=SHARES[0]).json()["recovery_token"]
        with patch.object(self.auth, "hash_password_with_salt", recording_hash):
            response = self.client.post(
                "/auth/reset-password", json={"recovery_token": token, "new_password": "password456"}
            )
        self.assertEqual(response.status_code, 200, response.text)
        self.assertEqual(on_loop, [False])

    def test_full_recovery_flow_resets_password_and_revokes_sessions(self):
        token = self.recover(part1=SHARES[0]).json()["recovery_token"]
        response = self.client.post(
            "/auth/reset-password", json={"recovery_token": token, "new_password": "password456"}
        )
        self.assertEqual(response.status_code, 200, response.text)
        ok = self.client.post("/auth/login", json={"username": "dave", "password": "password456"})
        self.assertEqual(ok.status_code, 200)


if __name__ == "__main__":
    unittest.main()
