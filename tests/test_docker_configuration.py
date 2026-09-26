from pathlib import Path
import unittest


class DockerConfigurationTests(unittest.TestCase):
    def test_compose_persists_database_and_uploaded_files(self):
        compose = Path("compose.yaml").read_text(encoding="utf-8")

        self.assertIn("DATABASE_PATH: /app/data/messenger.db", compose)
        self.assertIn("messenger_data:/app/data", compose)
        self.assertIn("messenger_static:/app/static", compose)

    def test_image_installs_runtime_tools_and_starts_backend(self):
        dockerfile = Path("Dockerfile").read_text(encoding="utf-8")

        self.assertIn("ffmpeg ssss", dockerfile)
        self.assertIn('CMD ["uvicorn", "server.main:app"', dockerfile)


if __name__ == "__main__":
    unittest.main()
