import logging
import os

logger = logging.getLogger(__name__)

# Origins of the Vite dev server and preview. Production serves the frontend and the API
# from one origin behind nginx, so it needs no CORS entries at all.
DEFAULT_CORS_ORIGINS = (
    "http://localhost:5173",
    "http://127.0.0.1:5173",
    "http://localhost:4173",
    "http://127.0.0.1:4173",
)


def _split(value: str) -> list[str]:
    return [item.strip() for item in value.split(",") if item.strip()]


def cors_origins(environ=None) -> list[str]:
    value = (environ if environ is not None else os.environ).get("CORS_ORIGINS")
    if value is None or not value.strip():
        return list(DEFAULT_CORS_ORIGINS)
    origins = [origin.rstrip("/") for origin in _split(value)]
    if "*" in origins:
        logger.warning("CORS_ORIGINS=* is ignored: credentialed requests need explicit origins")
        origins = [origin for origin in origins if origin != "*"]
    return origins


def allowed_hosts(environ=None) -> list[str] | None:
    """Hosts accepted in the Host header; None disables the check."""
    value = (environ if environ is not None else os.environ).get("ALLOWED_HOSTS", "")
    hosts = _split(value)
    return hosts or None
