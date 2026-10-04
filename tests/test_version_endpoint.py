"""The About screen of the frontend reads the server version from GET /version."""
import unittest
from unittest.mock import patch

from fastapi.testclient import TestClient

from server.main import app
from server.version import __version__


class VersionEndpointTests(unittest.TestCase):
    def test_reports_the_server_version_without_authentication(self):
        response = TestClient(app).get("/version")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.json()["version"], __version__)
    def test_reports_the_build_commit(self):
        with patch.dict("os.environ", {"APP_COMMIT": "abc1234def"}):
            self.assertEqual(TestClient(app).get("/version").json()["commit"], "abc1234")


if __name__ == "__main__":
    unittest.main()