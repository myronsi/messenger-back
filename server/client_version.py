"""Client/server compatibility: version endpoint, X-Client-Api-Version checks and the per-version request counter."""
import json
import logging
import re
from collections import Counter
from threading import Lock

from server.config import min_client_api_version
from server.version import API_VERSION, __version__, build_commit

logger = logging.getLogger(__name__)

CLIENT_VERSION_HEADER = "x-client-version"
CLIENT_API_VERSION_HEADER = "x-client-api-version"

_VERSION_RE = re.compile(r"^(\d{1,6})\.(\d{1,6})\.(\d{1,6})(?:[-+][0-9A-Za-z.+-]{0,64})?$")
MAX_TRACKED_VERSIONS = 50
# Endpoints a client must always be able to reach to find out why it is rejected.
EXEMPT_PATHS = {"/", "/version", "/metrics"}


def parse_version(value: str | None) -> tuple[int, int, int] | None:
    match = _VERSION_RE.match((value or "").strip())
    if not match:
        return None
    return tuple(int(part) for part in match.groups())


def version_info() -> dict:
    return {"version": __version__, "commit": build_commit()}


def hello_event(environ=None) -> dict:
    """First WebSocket event: lets clients that reconnect after a deploy notice the new contract."""
    return {
        "type": "hello",
        "api_version": API_VERSION,
        "min_client_api_version": min_client_api_version(environ),
    }


def check_client_api_version(value: str, environ=None) -> str | None:
    """Return None when the client may continue, otherwise a human readable reason."""
    client = parse_version(value)
    if client is None:
        return "invalid"
    server = parse_version(API_VERSION)
    minimum = parse_version(min_client_api_version(environ))
    if client[0] != server[0]:
        return f"API major version {client[0]} is not supported, this server speaks {server[0]}.x"
    if client < minimum:
        return f"API version {value} is older than the minimum supported {min_client_api_version(environ)}"
    return None


class ClientVersionCounter:
    """Requests per client API version. Values come from the client, so the number of series is capped."""

    def __init__(self, limit: int = MAX_TRACKED_VERSIONS):
        self._limit = limit
        self._counts: Counter = Counter()
        self._lock = Lock()

    def record(self, value: str | None) -> str:
        label = "none" if not value else value if parse_version(value) else "invalid"
        with self._lock:
            if label not in self._counts and len(self._counts) >= self._limit:
                label = "other"
            first_seen = label not in self._counts
            self._counts[label] += 1
        if first_seen:
            logger.info("First request from client API version %s", label)
        return label

    def snapshot(self) -> dict[str, int]:
        with self._lock:
            return dict(self._counts)

    def render_prometheus(self) -> str:
        lines = [
            "# HELP messenger_client_api_requests_total HTTP requests per X-Client-Api-Version.",
            "# TYPE messenger_client_api_requests_total counter",
        ]
        for label, count in sorted(self.snapshot().items()):
            lines.append(f'messenger_client_api_requests_total{{client_api_version="{label}"}} {count}')
        return "\n".join(lines) + "\n"


client_versions = ClientVersionCounter()


def _problem(status: int, code: str, title: str, detail: str, environ=None) -> tuple[bytes, list]:
    body = {
        "type": "about:blank",
        "title": title,
        "status": status,
        "detail": detail,
        "code": code,
        "api_version": API_VERSION,
        "min_client_api_version": min_client_api_version(environ),
    }
    payload = json.dumps(body).encode()
    headers = [(b"content-type", b"application/problem+json"), (b"content-length", str(len(payload)).encode())]
    return payload, headers


class ClientVersionMiddleware:
    """Counts and logs client versions and answers 426 to clients built for an unsupported API contract.

    Requests without X-Client-Api-Version (old clients, curl, health checks) are let through.
    """

    def __init__(self, app):
        self.app = app

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http" or scope["method"] == "OPTIONS":
            await self.app(scope, receive, send)
            return

        headers = {key.decode("latin-1").lower(): value.decode("latin-1") for key, value in scope["headers"]}
        api_version = headers.get(CLIENT_API_VERSION_HEADER)
        app_version = headers.get(CLIENT_VERSION_HEADER)
        label = client_versions.record(api_version)
        if scope["path"] in EXEMPT_PATHS or api_version is None:
            await self.app(scope, receive, send)
            return

        reason = check_client_api_version(api_version)
        if reason is None:
            await self.app(scope, receive, send)
            return

        logger.warning(
            "Rejected %s %s: client_api_version=%s client_version=%s",
            scope["method"], scope["path"], label, (app_version or "none")[:32],
        )
        if reason == "invalid":
            status, code, title = 400, "invalid_client_version", "Invalid client version"
            reason = "X-Client-Api-Version must look like 2.4.0"
        else:
            status, code, title = 426, "client_outdated", "Upgrade Required"
        payload, response_headers = _problem(status, code, title, reason)
        await send({"type": "http.response.start", "status": status, "headers": response_headers})
        await send({"type": "http.response.body", "body": payload})
