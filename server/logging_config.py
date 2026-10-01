import logging
import os
import re

_SENSITIVE_QUERY = re.compile(
    r"([?&](?:token|access_token|refresh_token|recovery_token|password)=)[^&\s\"']*",
    re.IGNORECASE,
)
_configured = False


def _redact(value):
    if isinstance(value, str):
        return _SENSITIVE_QUERY.sub(r"\1[REDACTED]", value)
    return value


class RedactSecretsFilter(logging.Filter):
    """Masks credentials passed in URLs, e.g. /ws/chat/0?token=..., before they are written."""

    def filter(self, record: logging.LogRecord) -> bool:
        record.msg = _redact(record.msg)
        if isinstance(record.args, tuple):
            record.args = tuple(_redact(arg) for arg in record.args)
        elif isinstance(record.args, dict):
            record.args = {key: _redact(arg) for key, arg in record.args.items()}
        return True


def configure_logging() -> None:
    """Single logging setup for the whole server; modules only call logging.getLogger(__name__)."""
    global _configured
    if _configured:
        return
    _configured = True

    level_name = os.environ.get("LOG_LEVEL", "INFO").upper()
    level = logging.getLevelName(level_name)
    if not isinstance(level, int):
        level = logging.INFO

    root = logging.getLogger()
    if not root.handlers:
        logging.basicConfig(
            level=level,
            format="%(asctime)s %(levelname)s %(name)s: %(message)s",
        )
    else:
        root.setLevel(level)

    redactor = RedactSecretsFilter()
    for handler in root.handlers:
        handler.addFilter(redactor)
    for name in ("uvicorn", "uvicorn.error", "uvicorn.access"):
        for handler in logging.getLogger(name).handlers:
            handler.addFilter(redactor)
