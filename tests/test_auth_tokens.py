"""Token hardening tests (MSGC-36).

The token-format tests need no database. The single-use recovery and 2FA
challenge tests need a disposable PostgreSQL database in TEST_DATABASE_URL.
"""
import os
import subprocess
import sys
import unittest
from datetime import timedelta

os.environ.setdefault("SECRET_KEY", "test-only-secret-key-not-for-production-use")
TEST_DATABASE_URL = os.environ.get("TEST_DATABASE_URL")


class SecretKeyTests(unittest.TestCase):
    def test_missing_or_short_key_is_rejected(self):
        from server.tokens import load_secret_key

        with self.assertRaises(RuntimeError):
            load_secret_key({})
        with self.assertRaises(RuntimeError):
            load_secret_key({"SECRET_KEY": "too-short"})
        self.assertEqual(load_secret_key({"SECRET_KEY": "x" * 32}), "x" * 32)

    def test_application_import_fails_without_secret_key(self):
        env = {k: v for k, v in os.environ.items() if k != "SECRET_KEY"}
        result = subprocess.run(
            [sys.executable, "-c", "import server.tokens"],
            env=env,
            capture_output=True,
            text=True,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("SECRET_KEY", result.stderr)

    def test_encryption_key_differs_from_signing_key(self):
        import base64

        from server.tokens import SECRET_KEY, derive_fernet_key

        derived = derive_fernet_key(SECRET_KEY)
        self.assertNotEqual(derived, base64.urlsafe_b64encode(SECRET_KEY.encode()[:32].ljust(32)))
        self.assertEqual(derived, derive_fernet_key(SECRET_KEY))
        self.assertNotEqual(derived, derive_fernet_key(SECRET_KEY + "x"))

    def test_totp_secret_encrypted_under_another_key_is_unusable_not_fatal(self):
        from cryptography.fernet import Fernet

        from server.routes import auth

        foreign = Fernet(Fernet.generate_key()).encrypt(b"JBSWY3DPEHPK3PXP").decode()
        self.assertIsNone(auth.decrypt_secret(foreign))
        self.assertEqual(auth.decrypt_secret(auth.encrypt_secret("JBSWY3DPEHPK3PXP")), "JBSWY3DPEHPK3PXP")


class TokenFormatTests(unittest.TestCase):
    def test_token_round_trip_has_type_audience_and_utc_expiry(self):
        from server import tokens

        token = tokens.create_token(tokens.TOKEN_ACCESS, 7, timedelta(minutes=5), sid="s1")
        payload = tokens.decode_token(token, tokens.TOKEN_ACCESS)

        self.assertEqual(payload["sub"], "7")
        self.assertEqual(payload["sid"], "s1")
        self.assertEqual(payload["type"], "access")
        self.assertEqual(payload["aud"], "messenger:access")
        self.assertGreater(payload["exp"], tokens.utc_now().timestamp())

    def test_token_of_one_type_is_rejected_as_another(self):
        from jose import JWTError

        from server import tokens

        for issued, expected in (
            (tokens.TOKEN_RECOVERY, tokens.TOKEN_ACCESS),
            (tokens.TOKEN_TWO_FACTOR, tokens.TOKEN_ACCESS),
            (tokens.TOKEN_ACCESS, tokens.TOKEN_RECOVERY),
        ):
            token = tokens.create_token(issued, 1, timedelta(minutes=5))
            with self.assertRaises(JWTError):
                tokens.decode_token(token, expected)

    def test_expired_and_forged_tokens_are_rejected(self):
        from jose import JWTError, jwt

        from server import tokens

        expired = tokens.create_token(tokens.TOKEN_ACCESS, 1, timedelta(seconds=-5))
        with self.assertRaises(JWTError):
            tokens.decode_token(expired, tokens.TOKEN_ACCESS)

        forged = jwt.encode(
            {"sub": "1", "type": "access", "aud": "messenger:access", "exp": 4102444800},
            "some-other-key-that-the-attacker-guessed-0123",
            algorithm=tokens.ALGORITHM,
        )
        with self.assertRaises(JWTError):
            tokens.decode_token(forged, tokens.TOKEN_ACCESS)

    def test_legacy_untyped_token_is_rejected(self):
        from jose import JWTError, jwt

        from server import tokens

        legacy = jwt.encode({"sub": "1", "sid": "s", "exp": 4102444800}, tokens.SECRET_KEY, algorithm=tokens.ALGORITHM)
        with self.assertRaises(JWTError):
            tokens.decode_token(legacy, tokens.TOKEN_ACCESS)


@unittest.skipUnless(TEST_DATABASE_URL, "TEST_DATABASE_URL is not set")
class ServerSideTokenRecordTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        import server.database as database

        cls.database = database
        cls.original_url = database.DATABASE_URL
        database.DATABASE_URL = TEST_DATABASE_URL
        database.setup_database()

    @classmethod
    def tearDownClass(cls):
        cls.database.DATABASE_URL = cls.original_url

    def setUp(self):
        self.conn = self.database.get_connection()
        self.cursor = self.conn.cursor()
        self.cursor.execute("TRUNCATE recovery_tokens, two_factor_challenges, users RESTART IDENTITY CASCADE")
        self.cursor.execute("INSERT INTO users (username, display_name, password) VALUES ('alice', 'Alice', 'x')")
        self.user_id = self.cursor.lastrowid
        self.conn.commit()

    def tearDown(self):
        self.conn.close()

    def test_recovery_token_works_exactly_once(self):
        from server import tokens

        token = tokens.issue_recovery_token(self.cursor, self.user_id, timedelta(minutes=5))
        self.conn.commit()

        self.assertEqual(tokens.consume_recovery_token(self.cursor, token), self.user_id)
        self.conn.commit()
        self.assertIsNone(tokens.consume_recovery_token(self.cursor, token))

    def test_validly_signed_recovery_token_not_issued_by_server_is_rejected(self):
        from server import tokens

        forged = tokens.create_token(tokens.TOKEN_RECOVERY, self.user_id, timedelta(minutes=5), jti="never-issued")
        self.assertIsNone(tokens.consume_recovery_token(self.cursor, forged))

        without_jti = tokens.create_token(tokens.TOKEN_RECOVERY, self.user_id, timedelta(minutes=5))
        self.assertIsNone(tokens.consume_recovery_token(self.cursor, without_jti))

    def test_expired_recovery_token_is_rejected(self):
        from server import tokens

        token = tokens.issue_recovery_token(self.cursor, self.user_id, timedelta(seconds=-5))
        self.assertIsNone(tokens.consume_recovery_token(self.cursor, token))

    def test_two_factor_challenge_is_bound_to_server_record_and_attempt_limited(self):
        from server import tokens

        challenge = tokens.issue_two_factor_challenge(self.cursor, self.user_id, timedelta(minutes=5))
        self.conn.commit()

        for _ in range(tokens.TWO_FACTOR_MAX_ATTEMPTS):
            attempt = tokens.begin_two_factor_attempt(self.cursor, challenge)
            self.assertEqual(attempt[0], self.user_id)
        self.assertIsNone(tokens.begin_two_factor_attempt(self.cursor, challenge))

    def test_two_factor_challenge_cannot_be_reused_after_success(self):
        from server import tokens

        challenge = tokens.issue_two_factor_challenge(self.cursor, self.user_id, timedelta(minutes=5))
        _, jti_hash = tokens.begin_two_factor_attempt(self.cursor, challenge)
        tokens.finish_two_factor_challenge(self.cursor, jti_hash)

        self.assertIsNone(tokens.begin_two_factor_attempt(self.cursor, challenge))

    def test_forged_two_factor_challenge_is_rejected(self):
        from server import tokens

        forged = tokens.create_token(tokens.TOKEN_TWO_FACTOR, self.user_id, timedelta(minutes=5), jti="never-issued")
        self.assertIsNone(tokens.begin_two_factor_attempt(self.cursor, forged))



@unittest.skipUnless(TEST_DATABASE_URL, "TEST_DATABASE_URL is not set")
class AuthEndpointTests(unittest.TestCase):
    PASSWORD = "Passw0rd!x"
    TOTP_SECRET = "JBSWY3DPEHPK3PXP"

    @classmethod
    def setUpClass(cls):
        import server.database as database

        cls.database = database
        cls.original_url = database.DATABASE_URL
        database.DATABASE_URL = TEST_DATABASE_URL
        database.setup_database()

    @classmethod
    def tearDownClass(cls):
        cls.database.DATABASE_URL = cls.original_url

    def setUp(self):
        from fastapi.testclient import TestClient

        from server.main import app
        from server.routes import auth

        self.auth = auth
        self.client = TestClient(app)
        conn = self.database.get_connection()
        cursor = conn.cursor()
        cursor.execute("TRUNCATE users RESTART IDENTITY CASCADE")
        cursor.execute(
            "INSERT INTO users (username, display_name, password) VALUES ('bob', 'Bob', ?)",
            (auth.hash_password_with_salt(self.PASSWORD),),
        )
        self.user_id = cursor.lastrowid
        cursor.execute(
            "INSERT INTO user_security_settings (user_id, two_factor_enabled, two_factor_secret) VALUES (?, ?, ?)",
            (self.user_id, True, auth.encrypt_secret(self.TOTP_SECRET)),
        )
        conn.commit()
        conn.close()

    def current_code(self):
        import time

        return self.auth.hotp(self.TOTP_SECRET, int(time.time() // 30))

    def start_login(self):
        response = self.client.post("/auth/login", json={"username": "bob", "password": self.PASSWORD})
        self.assertEqual(response.status_code, 200)
        return response.json()["login_challenge"]

    def access_headers(self):
        response = self.client.post("/auth/login/2fa", json={"login_challenge": self.start_login(), "code": self.current_code()})
        self.assertEqual(response.status_code, 200)
        return {"Authorization": f"Bearer {response.json()['access_token']}"}

    def change_stored_password(self, password):
        conn = self.database.get_connection()
        conn.cursor().execute(
            "UPDATE users SET password = ? WHERE id = ?", (self.auth.hash_password_with_salt(password), self.user_id)
        )
        conn.commit()
        conn.close()

    def stored_password_matches(self, password):
        conn = self.database.get_connection()
        cursor = conn.cursor()
        cursor.execute("SELECT password FROM users WHERE id = ?", (self.user_id,))
        stored = cursor.fetchone()["password"]
        conn.close()
        return self.auth.verify_password(stored, password)

    def test_password_change_does_not_overwrite_a_concurrent_change(self):
        from unittest.mock import patch

        headers = self.access_headers()
        hash_password = self.auth.hash_password_with_salt

        def hash_while_another_request_changes_it(password):
            self.change_stored_password("Concurrent0!x")
            return hash_password(password)

        with patch.object(self.auth, "hash_password_with_salt", hash_while_another_request_changes_it):
            response = self.client.post(
                "/auth/me/password",
                json={"current_password": self.PASSWORD, "new_password": "NewPassw0rd!y"},
                headers=headers,
            )
        self.assertEqual(response.status_code, 409, response.text)
        self.assertTrue(self.stored_password_matches("Concurrent0!x"))

    def test_two_factor_disable_rejects_a_password_changed_during_verification(self):
        from unittest.mock import patch

        headers = self.access_headers()
        verify_password = self.auth.verify_password

        def verify_while_another_request_changes_it(stored, provided):
            result = verify_password(stored, provided)
            self.change_stored_password("Concurrent0!x")
            return result

        with patch.object(self.auth, "verify_password", verify_while_another_request_changes_it):
            response = self.client.post(
                "/auth/me/2fa/disable", json={"password": self.PASSWORD, "code": self.current_code()}, headers=headers
            )
        self.assertEqual(response.status_code, 409, response.text)
        conn = self.database.get_connection()
        cursor = conn.cursor()
        cursor.execute("SELECT two_factor_enabled FROM user_security_settings WHERE user_id = ?", (self.user_id,))
        self.assertTrue(cursor.fetchone()["two_factor_enabled"])
        conn.close()

    def test_challenge_is_locked_after_too_many_wrong_codes(self):
        from server.tokens import TWO_FACTOR_MAX_ATTEMPTS

        challenge = self.start_login()
        for _ in range(TWO_FACTOR_MAX_ATTEMPTS):
            response = self.client.post("/auth/login/2fa", json={"login_challenge": challenge, "code": "000000"})
            self.assertEqual(response.status_code, 401)

        response = self.client.post("/auth/login/2fa", json={"login_challenge": challenge, "code": self.current_code()})
        self.assertEqual(response.status_code, 401)

    def test_challenge_is_single_use_and_not_accepted_as_access_token(self):
        challenge = self.start_login()
        self.assertEqual(
            self.client.get("/auth/me", headers={"Authorization": f"Bearer {challenge}"}).status_code, 401
        )

        code = self.current_code()
        response = self.client.post("/auth/login/2fa", json={"login_challenge": challenge, "code": code})
        self.assertEqual(response.status_code, 200)
        access_token = response.json()["access_token"]
        self.assertEqual(
            self.client.get("/auth/me", headers={"Authorization": f"Bearer {access_token}"}).status_code, 200
        )
        self.assertEqual(
            self.client.post("/auth/login/2fa", json={"login_challenge": challenge, "code": code}).status_code, 401
        )

    def test_reset_password_accepts_a_recovery_token_once_and_rejects_forgeries(self):
        from server import tokens

        conn = self.database.get_connection()
        recovery_token = tokens.issue_recovery_token(conn.cursor(), self.user_id, timedelta(minutes=5))
        conn.commit()
        conn.close()

        self.assertEqual(
            self.client.get("/auth/me", headers={"Authorization": f"Bearer {recovery_token}"}).status_code, 401
        )
        body = {"recovery_token": recovery_token, "new_password": "NewPassw0rd!y"}
        self.assertEqual(self.client.post("/auth/reset-password", json=body).status_code, 200)
        self.assertEqual(self.client.post("/auth/reset-password", json=body).status_code, 401)

        forged = tokens.create_token(tokens.TOKEN_RECOVERY, self.user_id, timedelta(minutes=5), jti="x")
        response = self.client.post(
            "/auth/reset-password", json={"recovery_token": forged, "new_password": "NewPassw0rd!z"}
        )
        self.assertEqual(response.status_code, 401)

if __name__ == "__main__":
    unittest.main()
