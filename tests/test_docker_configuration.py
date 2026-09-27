from pathlib import Path
import unittest


class DockerConfigurationTests(unittest.TestCase):
    def test_compose_runs_and_persists_postgresql_and_uploaded_files(self):
        compose = Path("compose.yaml").read_text(encoding="utf-8")

        self.assertIn("postgres:16-alpine", compose)
        self.assertIn("DATABASE_URL: postgresql://messenger:messenger@postgres:5432/messenger", compose)
        self.assertIn("messenger_postgres:/var/lib/postgresql/data", compose)
        self.assertIn("messenger_static:/app/static", compose)

    def test_image_installs_runtime_tools_and_starts_backend(self):
        dockerfile = Path("Dockerfile").read_text(encoding="utf-8")

        self.assertIn("ffmpeg ssss", dockerfile)
        self.assertIn("COPY static ./static", dockerfile)
        self.assertIn("COPY static /opt/default-static", dockerfile)
        self.assertIn(
            "cp -n /opt/default-static/avatars/default.jpg /app/static/avatars/default.jpg",
            dockerfile,
        )
        self.assertIn("exec uvicorn server.main:app", dockerfile)


if __name__ == "__main__":
    unittest.main()
