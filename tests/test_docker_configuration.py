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
        for name in ("default.jpg", "deleted.jpg", "group.png"):
            self.assertIn(f"/opt/default-static/avatars/{name}", dockerfile)
        self.assertIn("cp -f", dockerfile)
        self.assertIn("exec uvicorn server.main:app", dockerfile)
        self.assertIn("ARG COMMIT", dockerfile)
        self.assertIn("ENV APP_COMMIT=$COMMIT", dockerfile)

    def test_builtin_avatars_are_valid_images(self):
        from server.media_access import PUBLIC_FILES

        signatures = {
            ".jpg": b"\xff\xd8\xff",
            ".png": b"\x89PNG\r\n\x1a\n",
        }
        for relative in sorted(PUBLIC_FILES):
            path = Path("static") / relative
            self.assertTrue(path.is_file(), f"{path} is missing")
            data = path.read_bytes()
            self.assertGreater(len(data), 2000, f"{path} looks like a stub")
            self.assertTrue(data.startswith(signatures[path.suffix]), f"{path} has a wrong signature")


if __name__ == "__main__":
    unittest.main()
