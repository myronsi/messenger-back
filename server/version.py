import os

__version__ = "0.5.3"  # x-release-please-version

# Contract version of the HTTP/WebSocket API (see docs/versioning.md); the Python API is contract v1.
# Bump it only together with the API change, not with every release.
API_VERSION = "1.0.0"


def build_commit(environ=None) -> str:
    """Short commit hash baked into the image at build time (APP_COMMIT); 'unknown' for local runs."""
    value = (environ if environ is not None else os.environ).get("APP_COMMIT", "").strip()
    return value[:7] if value else "unknown"
