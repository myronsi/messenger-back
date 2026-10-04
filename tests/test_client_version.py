"""Version endpoint, client version checks (426 client_outdated), metrics and the WebSocket hello event."""
import unittest
from unittest.mock import patch

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware
from starlette.testclient import TestClient

from server.client_version import (
    ClientVersionCounter,
    ClientVersionMiddleware,
    check_client_api_version,
    hello_event,
    parse_version,
    validate_configuration,
    version_info,
)
from server.config import metrics_enabled, min_client_api_version
from server.version import API_VERSION, __version__, build_commit


class VersionTests(unittest.TestCase):
    def test_build_commit_is_short_and_defaults_to_unknown(self):
        self.assertEqual(build_commit({"APP_COMMIT": "0123456789abcdef"}), "0123456")
        self.assertEqual(build_commit({}), "unknown")
        self.assertEqual(build_commit({"APP_COMMIT": " "}), "unknown")

    def test_version_info_shape(self):
        with patch.dict("os.environ", {"APP_COMMIT": "abc1234ffff"}):
            self.assertEqual(version_info(), {"version": __version__, "commit": "abc1234"})

    def test_min_version_comes_from_configuration(self):
        self.assertEqual(min_client_api_version({}), "1.0.0")
        self.assertEqual(min_client_api_version({"MIN_CLIENT_API_VERSION": " 1.2.0 "}), "1.2.0")
        self.assertFalse(metrics_enabled({}))
        self.assertTrue(metrics_enabled({"METRICS_ENABLED": "true"}))

    def test_parse_version(self):
        self.assertEqual(parse_version("2.4.0")[:3], (2, 4, 0))
        self.assertEqual(parse_version("2.0.0-alpha.3")[:3], (2, 0, 0))
        self.assertEqual(parse_version("1.0.0+build.5"), parse_version("1.0.0"))
        for bad in (None, "", "2.4", "latest", "2.4.0\nx", "a.b.c", "1.0.0-", "1.0.0-a..b", "1." + "0" * 80 + ".0"):
            self.assertIsNone(parse_version(bad))

    def test_prereleases_sort_below_their_release_by_semver_precedence(self):
        ordered = ["1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2",
                   "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0"]
        keys = [parse_version(value) for value in ordered]
        self.assertEqual(keys, sorted(keys))
        self.assertEqual(len(set(keys)), len(keys))

    def test_invalid_minimum_is_rejected_at_startup(self):
        validate_configuration({})
        validate_configuration({"MIN_CLIENT_API_VERSION": "1.0.0"})
        for bad in ("1.2", "latest", "2.0.0", "0.9.0", "1.99.0"):
            with self.assertRaises(ValueError, msg=bad):
                validate_configuration({"MIN_CLIENT_API_VERSION": bad})

    def test_hello_event(self):
        self.assertEqual(
            hello_event({"MIN_CLIENT_API_VERSION": "1.1.0"}),
            {"type": "hello", "api_version": API_VERSION, "min_client_api_version": "1.1.0"},
        )


class CompatibilityTests(unittest.TestCase):
    def test_rules(self):
        env = {"MIN_CLIENT_API_VERSION": "1.2.0"}
        self.assertIsNone(check_client_api_version("1.2.0", env))
        self.assertIsNone(check_client_api_version("1.9.4", env))
        self.assertIn("older", check_client_api_version("1.1.9", env))
        self.assertIn("major", check_client_api_version("2.0.0", env))
        self.assertIn("older", check_client_api_version("1.2.0-rc.1", env))
        self.assertIn("older", check_client_api_version("1.0.0-alpha.3", {}))
        self.assertIsNone(check_client_api_version("1.0.0+build.7", {}))
        self.assertEqual(check_client_api_version("nonsense", env), "invalid")


class CounterTests(unittest.TestCase):
    def test_counts_per_version_with_capped_cardinality(self):
        counter = ClientVersionCounter(limit=2)
        for value in ("1.0.0", "1.0.0", None, "junk", "1.1.0", "1.2.0"):
            counter.record(value)
        self.assertEqual(counter.snapshot(), {"1.0.0": 2, "none": 1, "other": 3})
        self.assertIn('client_api_version="1.0.0"} 2', counter.render_prometheus())

    def test_each_new_client_pair_is_logged_once_with_both_versions(self):
        counter = ClientVersionCounter()
        with self.assertLogs("server.client_version", level="INFO") as logs:
            counter.record("1.0.0", "0.6.1")
            counter.record("1.0.0", "0.6.1")
            counter.record("1.0.0", "0.6.2")
            counter.record("1.0.0", "bad value")
        self.assertEqual(len(logs.output), 3)
        self.assertIn("client_version=0.6.1 client_api_version=1.0.0", logs.output[0])
        self.assertIn("client_version=invalid", logs.output[2])


class MiddlewareTests(unittest.TestCase):
    def setUp(self):
        app = FastAPI()
        app.add_middleware(ClientVersionMiddleware)
        app.add_middleware(CORSMiddleware, allow_origins=["https://app.example.com"],
                           allow_methods=["GET"], allow_headers=["X-Client-Api-Version"])

        @app.get("/")
        def root():
            return {"ok": True}

        @app.get("/version")
        def version():
            return version_info()

        @app.get("/things")
        def things():
            return {"things": []}

        self.client = TestClient(app)
        patcher = patch.dict("os.environ", {"MIN_CLIENT_API_VERSION": "1.0.0"})
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_requests_without_the_header_are_served(self):
        self.assertEqual(self.client.get("/things").status_code, 200)

    def test_supported_versions_are_served(self):
        for value in ("1.0.0", "1.7.2"):
            self.assertEqual(self.client.get("/things", headers={"X-Client-Api-Version": value}).status_code, 200)

    def test_other_major_gets_426_problem_json(self):
        response = self.client.get(
            "/things",
            headers={"X-Client-Api-Version": "2.0.0", "X-Client-Version": "0.6.1", "Origin": "https://app.example.com"},
        )
        self.assertEqual(response.status_code, 426)
        self.assertEqual(response.headers["content-type"], "application/problem+json")
        self.assertEqual(response.headers["access-control-allow-origin"], "https://app.example.com")
        body = response.json()
        self.assertEqual(body["code"], "client_outdated")
        self.assertEqual(body["status"], 426)
        self.assertEqual(body["min_client_api_version"], "1.0.0")

    def test_version_below_minimum_gets_426(self):
        with patch.dict("os.environ", {"MIN_CLIENT_API_VERSION": "1.3.0"}):
            response = self.client.get("/things", headers={"X-Client-Api-Version": "1.2.9"})
        self.assertEqual(response.status_code, 426)
        self.assertEqual(response.json()["code"], "client_outdated")

    def test_malformed_version_is_a_bad_request(self):
        response = self.client.get("/things", headers={"X-Client-Api-Version": "latest"})
        self.assertEqual(response.status_code, 400)
        self.assertEqual(response.json()["code"], "invalid_client_version")

    def test_version_endpoint_is_reachable_by_outdated_clients(self):
        response = self.client.get("/version", headers={"X-Client-Api-Version": "0.1.0"})
        self.assertEqual(response.status_code, 200)
        self.assertEqual(self.client.get("/", headers={"X-Client-Api-Version": "9.0.0"}).status_code, 200)

    def test_preflight_is_not_blocked(self):
        response = self.client.options(
            "/things",
            headers={"Origin": "https://app.example.com", "Access-Control-Request-Method": "GET",
                     "X-Client-Api-Version": "9.0.0"},
        )
        self.assertEqual(response.status_code, 200)


if __name__ == "__main__":
    unittest.main()
